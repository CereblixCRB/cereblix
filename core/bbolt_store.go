package core

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

// --- Materialized history rows (schema v2) ----------------------------------
// Each addrTx entry's VALUE holds the HistoryItem fields for that (address, tx),
// so addrHistory answers /api/history with a pure index scan and NEVER loads or
// JSON-decodes a block. Height comes from the key, so the value stores the other
// six fields in a compact binary layout (no JSON, no reflection):
//   Time(8 BE) | Amount(8 BE) | Fee(8 BE) | lp(TxID) | lp(From) | lp(To)
// where lp = 1-byte length prefix + bytes (TxID hex<=64, addresses<=44 -> <256).
// A nil/short/garbled value (old store, pre-backfill) makes the reader fall back
// to the block-load path, so the new binary is correct on an un-migrated DB and
// the OLD binary (which only reads the key) is unaffected on a migrated DB.

func appendLPStr(b []byte, s string) []byte {
	b = append(b, byte(len(s)))
	return append(b, s...)
}

func readLPStr(b []byte) (string, []byte, bool) {
	if len(b) < 1 {
		return "", nil, false
	}
	n := int(b[0])
	b = b[1:]
	if len(b) < n {
		return "", nil, false
	}
	return string(b[:n]), b[n:], true
}

func encodeHistRow(it HistoryItem) []byte {
	b := make([]byte, 0, 24+3+len(it.TxID)+len(it.From)+len(it.To))
	b = append(b, u64be(it.Time)...)
	b = append(b, u64be(it.Amount)...)
	b = append(b, u64be(it.Fee)...)
	b = appendLPStr(b, it.TxID)
	b = appendLPStr(b, it.From)
	b = appendLPStr(b, it.To)
	return b
}

// decodeHistRow parses a row value; height is taken from the addrTx key. Returns
// ok=false for a nil/short/garbled value so the caller can fall back to the block.
func decodeHistRow(v []byte, height uint64) (HistoryItem, bool) {
	if len(v) < 24 {
		return HistoryItem{}, false
	}
	it := HistoryItem{Height: height}
	it.Time = binary.BigEndian.Uint64(v[0:8])
	it.Amount = binary.BigEndian.Uint64(v[8:16])
	it.Fee = binary.BigEndian.Uint64(v[16:24])
	rest := v[24:]
	var ok bool
	if it.TxID, rest, ok = readLPStr(rest); !ok {
		return HistoryItem{}, false
	}
	if it.From, rest, ok = readLPStr(rest); !ok {
		return HistoryItem{}, false
	}
	if it.To, rest, ok = readLPStr(rest); !ok {
		return HistoryItem{}, false
	}
	return it, true
}

// putAddrRows writes the materialized HistoryItem row under each addrTx key a
// block touches (sender AND recipient, once for a self-send; coinbase by
// recipient only) — mirrors the old nil-value indexing but stores the row. Used
// by putBlockTx (live extend) and backfillRows (one-time migration).
func putAddrRows(at *bolt.Bucket, b *Block) error {
	for i, t := range b.Txs {
		coinbase := t.IsCoinbase()
		from := "coinbase"
		if !coinbase {
			f, ferr := t.FromAddr()
			if ferr != nil {
				continue
			}
			from = f
		}
		row := encodeHistRow(HistoryItem{TxID: t.ID(), Height: b.Height, Time: b.Time,
			From: from, To: t.To, Amount: t.Amount, Fee: t.Fee})
		if coinbase {
			if err := at.Put(addrTxKey(t.To, b.Height, i), row); err != nil {
				return err
			}
			continue
		}
		if err := at.Put(addrTxKey(from, b.Height, i), row); err != nil {
			return err
		}
		if t.To != from {
			if err := at.Put(addrTxKey(t.To, b.Height, i), row); err != nil {
				return err
			}
		}
	}
	return nil
}

// addrHistory serves History(addr) straight from the DB index: a reverse
// prefix-scan of addrTx (newest block first), emitting each block's txs in
// ascending index order so the result is byte-identical to the in-memory path.
// O(offset+limit) — it stops once the page is filled, never scanning the chain.
// Schema v2: the row value carries the HistoryItem, so a block is loaded ONLY as
// a fallback for a nil/garbled value (old store / pre-backfill).
func (s *blockStore) addrHistory(addr string, limit, offset int) []HistoryItem {
	if offset < 0 {
		offset = 0
	}
	out := make([]HistoryItem, 0, limit)
	prefix := []byte(addr)
	lp := len(prefix)
	s.db.View(func(tx *bolt.Tx) error {
		blocks := tx.Bucket(bkBlocks)
		cur := tx.Bucket(bkAddrTx).Cursor()
		// Position at this address's LAST key, then walk backwards.
		upper := append(append([]byte{}, prefix...), bytes.Repeat([]byte{0xFF}, 12)...)
		var k, v []byte
		if k, v = cur.Seek(upper); k == nil {
			k, v = cur.Last()
		} else {
			k, v = cur.Prev()
		}
		skipped := 0
		type entry struct {
			idx int
			val []byte
		}
		run := []entry{} // entries of the current block (collected descending)
		var runH uint64
		var blk *Block // lazily loaded ONLY on fallback
		flush := func() bool { // emit run ascending; returns false when limit hit
			if len(run) == 0 {
				return true
			}
			for i := len(run) - 1; i >= 0; i-- {
				e := run[i]
				if skipped < offset {
					skipped++
					continue
				}
				if len(out) >= limit {
					run = run[:0]
					return false
				}
				if it, ok := decodeHistRow(e.val, runH); ok {
					out = append(out, it)
					continue
				}
				// FALLBACK: nil/garbled row -> load the block and extract the tx.
				if blk == nil || blk.Height != runH {
					raw := blocks.Get(u64be(runH))
					if raw == nil {
						continue
					}
					var b Block
					if json.Unmarshal(raw, &b) != nil {
						continue
					}
					blk = &b
				}
				if e.idx < 0 || e.idx >= len(blk.Txs) {
					continue
				}
				t := blk.Txs[e.idx]
				from := "coinbase"
				if !t.IsCoinbase() {
					from, _ = t.FromAddr()
				}
				out = append(out, HistoryItem{TxID: t.ID(), Height: blk.Height, Time: blk.Time,
					From: from, To: t.To, Amount: t.Amount, Fee: t.Fee})
			}
			run = run[:0]
			return len(out) < limit
		}
		for k != nil && len(k) >= lp+12 && bytes.HasPrefix(k, prefix) {
			h := binary.BigEndian.Uint64(k[lp : lp+8])
			idx := int(binary.BigEndian.Uint32(k[lp+8 : lp+12]))
			if len(run) > 0 && h != runH {
				if !flush() {
					return nil
				}
				blk = nil
			}
			runH = h
			run = append(run, entry{idx: idx, val: append([]byte(nil), v...)}) // copy: cursor reuses v
			k, v = cur.Prev()
		}
		flush()
		return nil
	})
	return out
}

// findTx serves FindTx for confirmed txs from the DB index (mempool checked by the
// caller). Returns (loc, true) if found in a block.
func (s *blockStore) findTx(id string) (TxLocation, bool) {
	var loc TxLocation
	found := false
	s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bkTxIndex).Get([]byte(id))
		if v == nil || len(v) != 12 {
			return nil
		}
		h := binary.BigEndian.Uint64(v[:8])
		idx := int(binary.BigEndian.Uint32(v[8:]))
		raw := tx.Bucket(bkBlocks).Get(u64be(h))
		if raw == nil {
			return nil
		}
		var b Block
		if json.Unmarshal(raw, &b) != nil || idx < 0 || idx >= len(b.Txs) {
			return nil
		}
		loc = txToLocation(b.Txs[idx])
		loc.Height, loc.BlockHash, loc.Time = b.Height, b.Hash(), b.Time
		found = true
		return nil
	})
	return loc, found
}

// blockByHash serves BlockByHash from the DB hash->height index in O(log n).
func (s *blockStore) blockByHash(hash string) *Block {
	var b *Block
	s.db.View(func(tx *bolt.Tx) error {
		hv := tx.Bucket(bkBlockHash).Get([]byte(hash))
		if hv == nil {
			return nil
		}
		raw := tx.Bucket(bkBlocks).Get(hv)
		if raw == nil {
			return nil
		}
		var blk Block
		if json.Unmarshal(raw, &blk) != nil {
			return nil
		}
		b = &blk
		return nil
	})
	return b
}

// ExportBoltToJSONL dumps the bbolt store's blocks back to blocks.jsonl so an
// operator can roll back to the legacy jsonl binary. 2.3.0 rollback path.
func ExportBoltToJSONL(dir string) (int, error) {
	st, err := openBlockStore(filepath.Join(dir, "chain.db"))
	if err != nil {
		return 0, err
	}
	defer st.close()
	f, err := os.Create(filepath.Join(dir, "blocks.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	n := 0
	if err := st.forEachBlock(func(b *Block) error {
		raw, e := json.Marshal(b)
		if e != nil {
			return e
		}
		if _, e := w.Write(raw); e != nil {
			return e
		}
		if e := w.WriteByte('\n'); e != nil {
			return e
		}
		n++
		return nil
	}); err != nil {
		return n, err
	}
	return n, w.Flush()
}

// blockStore is the bbolt-backed chain storage for the 2.3.0 migration off
// blocks.jsonl. Schema is laid out CONTRACT-READY now (state/code/storage buckets
// exist though only blocks+indexes are populated in this slice) so the future
// smart-contract hard fork needs no storage redesign. Every block write is ONE
// bbolt transaction that updates the block AND its indexes atomically -> the
// indexes can never desync (ACID), and a crash mid-write rolls back cleanly.
//
// NOT yet wired into Chain — this slice is the standalone, tested storage layer.
type blockStore struct {
	db *bolt.DB
}

// Bucket names. code/storage are created but stay EMPTY until smart contracts
// (so the schema is forward-compatible without a later migration).
var (
	bkMeta      = []byte("meta")      // chain metadata (tip, cumwork, schema ver)
	bkBlocks    = []byte("blocks")    // height(8B BE) -> block JSON
	bkBlockHash = []byte("blockHash") // block hash -> height(8B BE)
	bkState     = []byte("state")     // address -> account {balance,nonce}  (filled in slice 2)
	bkCode      = []byte("code")      // contract address -> bytecode         (future: contracts)
	bkStorage   = []byte("storage")   // contract addr ++ slot -> value       (future: contracts)
	bkTxIndex   = []byte("txIndex")   // txid -> location {height,idx}
	bkAddrTx    = []byte("addrTx")    // address(44B) ++ height(8B) ++ idx(4B) -> HistoryItem row (schema>=2; nil in schema 1)
)

// storeSchemaVersion 2: addrTx values hold materialized HistoryItem rows (were
// nil in v1). The reader falls back to the block on a nil/short value, and the v1
// binary ignores the value entirely — so up/down-grade is store-compatible.
const storeSchemaVersion = 2

var allBuckets = [][]byte{bkMeta, bkBlocks, bkBlockHash, bkState, bkCode, bkStorage, bkTxIndex, bkAddrTx}

func u64be(x uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, x); return b }
func u32be(x uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, x); return b }

// txLocBytes encodes a (height,idx) transaction location: 8B height + 4B idx.
func txLocBytes(height uint64, idx int) []byte { return append(u64be(height), u32be(uint32(idx))...) }

// addrTxKey is address(fixed 44B) ++ height(8B BE) ++ idx(4B BE) so a cursor can
// Seek the address prefix and walk that address's txs in (height,idx) order.
func addrTxKey(addr string, height uint64, idx int) []byte {
	k := make([]byte, 0, len(addr)+12)
	k = append(k, addr...)
	k = append(k, u64be(height)...)
	k = append(k, u32be(uint32(idx))...)
	return k
}

// openBlockStore opens (creating if needed) the bbolt chain DB and ensures every
// bucket exists. The file is mmap'd, so for our small DB it lives in the OS page
// cache = RAM-speed reads, while staying durable across restarts.
func openBlockStore(path string) (*blockStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 0})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range allBuckets {
			if _, e := tx.CreateBucketIfNotExists(b); e != nil {
				return e
			}
		}
		m := tx.Bucket(bkMeta)
		if m.Get([]byte("schema")) == nil {
			if e := m.Put([]byte("schema"), u64be(storeSchemaVersion)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &blockStore{db: db}, nil
}

func (s *blockStore) close() error { return s.db.Close() }

// schema reads the stored schema version (0 if unset/legacy).
func (s *blockStore) schema() uint64 {
	var v uint64
	s.db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bkMeta).Get([]byte("schema")); len(b) == 8 {
			v = binary.BigEndian.Uint64(b)
		}
		return nil
	})
	return v
}

// setSchema stamps the schema version.
func (s *blockStore) setSchema(v uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bkMeta).Put([]byte("schema"), u64be(v))
	})
}

// backfillRows populates the materialized HistoryItem row for every addrTx entry
// of the given blocks (the schema 1->2 migration). Batched + idempotent (each Put
// overwrites with the same deterministic row), so a crash mid-run is harmless and
// a re-run is a no-op. Reads nothing from disk — works off the in-RAM blocks the
// chain already holds, so no 2x disk and no full rebuild.
func (s *blockStore) backfillRows(blocks []*Block) error {
	const batch = 500
	for i := 0; i < len(blocks); i += batch {
		end := i + batch
		if end > len(blocks) {
			end = len(blocks)
		}
		chunk := blocks[i:end]
		if err := s.db.Update(func(tx *bolt.Tx) error {
			at := tx.Bucket(bkAddrTx)
			for _, b := range chunk {
				if err := putAddrRows(at, b); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// putBlockTx writes block b and ALL its indexes inside the caller's write txn, so
// block + indexes commit atomically (no desync possible). tx indexed by id; addr
// indexed (with materialized HistoryItem rows) by sender AND recipient.
func putBlockTx(tx *bolt.Tx, b *Block) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	h := u64be(b.Height)
	if err := tx.Bucket(bkBlocks).Put(h, raw); err != nil {
		return err
	}
	if err := tx.Bucket(bkBlockHash).Put([]byte(b.Hash()), h); err != nil {
		return err
	}
	txi := tx.Bucket(bkTxIndex)
	for i, t := range b.Txs {
		if err := txi.Put([]byte(t.ID()), txLocBytes(b.Height, i)); err != nil {
			return err
		}
	}
	return putAddrRows(tx.Bucket(bkAddrTx), b)
}

// putMeta stamps tip (= last block height key) and cumwork in the meta bucket.
func putMeta(tx *bolt.Tx, cumwork *big.Int) error {
	m := tx.Bucket(bkMeta)
	if k, _ := tx.Bucket(bkBlocks).Cursor().Last(); k != nil {
		if e := m.Put([]byte("tip"), append([]byte(nil), k...)); e != nil {
			return e
		}
	}
	return m.Put([]byte("cumwork"), cumwork.Bytes())
}

// appendBlocks writes new blocks + their indexes + meta in ONE transaction (the
// common tip-extension path). Atomic: block and indexes commit together.
func (s *blockStore) appendBlocks(blocks []*Block, cumwork *big.Int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, b := range blocks {
			if err := putBlockTx(tx, b); err != nil {
				return err
			}
		}
		return putMeta(tx, cumwork)
	})
}

// rebuild clears the block + index buckets and rewrites the full chain in one
// transaction. Used on reorg (rare) and genesis init. state/code/storage/meta-schema
// are left untouched.
func (s *blockStore) rebuild(blocks []*Block, cumwork *big.Int) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{bkBlocks, bkBlockHash, bkTxIndex, bkAddrTx} {
			if err := tx.DeleteBucket(name); err != nil && err != bolt.ErrBucketNotFound {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		for _, b := range blocks {
			if err := putBlockTx(tx, b); err != nil {
				return err
			}
		}
		return putMeta(tx, cumwork)
	})
}

// forEachBlock iterates stored blocks in ascending height order in one read txn.
func (s *blockStore) forEachBlock(fn func(*Block) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		cur := tx.Bucket(bkBlocks).Cursor()
		for k, v := cur.First(); k != nil; k, v = cur.Next() {
			var b Block
			if err := json.Unmarshal(v, &b); err != nil {
				return err
			}
			if err := fn(&b); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *blockStore) getBlock(height uint64) (*Block, error) {
	var b *Block
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bkBlocks).Get(u64be(height))
		if raw == nil {
			return fmt.Errorf("no block at height %d", height)
		}
		var blk Block
		if e := json.Unmarshal(raw, &blk); e != nil { // custom UnmarshalJSON caches hash/id/fromAddr
			return e
		}
		b = &blk
		return nil
	})
	return b, err
}

// findTxLoc returns a transaction's (height, index) from the tx index.
func (s *blockStore) findTxLoc(txid string) (height uint64, idx int, ok bool) {
	s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bkTxIndex).Get([]byte(txid)); v != nil && len(v) == 12 {
			height = binary.BigEndian.Uint64(v[:8])
			idx = int(binary.BigEndian.Uint32(v[8:]))
			ok = true
		}
		return nil
	})
	return
}

// tipHeight returns the highest stored block height and whether any exist.
func (s *blockStore) tipHeight() (uint64, bool) {
	var h uint64
	var ok bool
	s.db.View(func(tx *bolt.Tx) error {
		k, _ := tx.Bucket(bkBlocks).Cursor().Last()
		if k != nil {
			h, ok = binary.BigEndian.Uint64(k), true
		}
		return nil
	})
	return h, ok
}

// cumWork reads the cached cumulative work from meta (set during migration/commit).
func (s *blockStore) cumWork() *big.Int {
	w := new(big.Int)
	s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bkMeta).Get([]byte("cumwork")); v != nil {
			w.SetBytes(v)
		}
		return nil
	})
	return w
}

// migrateFromJSONL imports an existing blocks.jsonl into an empty store, building
// all indexes + the cumwork meta, in batched write transactions. Idempotent guard:
// refuses if the store already has blocks (so a re-run can't double-import).
func (s *blockStore) migrateFromJSONL(jsonlPath string) (int, error) {
	if _, ok := s.tipHeight(); ok {
		return 0, fmt.Errorf("store already populated; refusing to migrate over it")
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)

	work := new(big.Int)
	n := 0
	const batch = 1000
	var pending []*Block
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		e := s.db.Update(func(tx *bolt.Tx) error {
			for _, b := range pending {
				if err := putBlockTx(tx, b); err != nil {
					return err
				}
			}
			return nil
		})
		if e == nil {
			pending = pending[:0]
		}
		return e
	}
	for sc.Scan() {
		var b Block
		if e := json.Unmarshal(sc.Bytes(), &b); e != nil {
			return n, fmt.Errorf("migrate: corrupt jsonl at block %d: %w", n, e)
		}
		t, e := b.TargetInt()
		if e != nil {
			return n, fmt.Errorf("migrate: bad target at height %d: %w", b.Height, e)
		}
		work.Add(work, WorkOf(t))
		pending = append(pending, &b)
		n++
		if len(pending) >= batch {
			if e := flush(); e != nil {
				return n, e
			}
		}
	}
	if e := sc.Err(); e != nil {
		return n, e
	}
	if e := flush(); e != nil {
		return n, e
	}
	// Stamp meta (tip + cumwork) in a final txn.
	err = s.db.Update(func(tx *bolt.Tx) error {
		m := tx.Bucket(bkMeta)
		if k, _ := tx.Bucket(bkBlocks).Cursor().Last(); k != nil {
			if e := m.Put([]byte("tip"), append([]byte(nil), k...)); e != nil {
				return e
			}
		}
		return m.Put([]byte("cumwork"), work.Bytes())
	})
	return n, err
}
