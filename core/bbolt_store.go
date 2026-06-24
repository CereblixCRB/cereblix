package core

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

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
	bkAddrTx    = []byte("addrTx")    // address(44B) ++ height(8B) ++ idx(4B) -> nil
)

const storeSchemaVersion = 1

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

// putBlockTx writes block b and ALL its indexes inside the caller's write txn, so
// block + indexes commit atomically (no desync possible). Mirrors the in-memory
// indexBlockLocked semantics: tx indexed by id; addr indexed by sender AND
// recipient (once for a self-send); coinbase by recipient only.
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
	txi, at := tx.Bucket(bkTxIndex), tx.Bucket(bkAddrTx)
	for i, t := range b.Txs {
		if err := txi.Put([]byte(t.ID()), txLocBytes(b.Height, i)); err != nil {
			return err
		}
		if t.IsCoinbase() {
			if err := at.Put(addrTxKey(t.To, b.Height, i), nil); err != nil {
				return err
			}
			continue
		}
		from, ferr := t.FromAddr()
		if ferr != nil {
			continue
		}
		if err := at.Put(addrTxKey(from, b.Height, i), nil); err != nil {
			return err
		}
		if t.To != from {
			if err := at.Put(addrTxKey(t.To, b.Height, i), nil); err != nil {
				return err
			}
		}
	}
	return nil
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
