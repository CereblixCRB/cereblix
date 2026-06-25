package core

import (
	"crypto/ed25519"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestHistoryRowsMatchFallback proves the schema-v2 materialized-row path is
// byte-identical to BOTH the jsonl reference AND the block-load fallback, and that
// backfillRows reconstructs the rows correctly. Covers multi-tx-per-block (A twice),
// self-send, coinbase (miner), and offset paging.
func TestHistoryRowsMatchFallback(t *testing.T) {
	dir := t.TempDir()
	c, err := NewChain(dir) // jsonl reference
	if err != nil {
		t.Fatal(err)
	}
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)
	A, B := AddrFromPub(pubA), AddrFromPub(pubB)
	c.state[A] = &Account{Balance: 1_000_000_000}
	c.state[B] = &Account{Balance: 1_000_000_000}
	c.recomputeSupplyLocked()
	miner := "crb1" + strings.Repeat("e", 40)
	mine := func() {
		b, e := c.BuildTemplate(miner)
		if e != nil {
			t.Fatal(e)
		}
		c.verifiedPow[b.Hash()] = true
		if e := c.AddBlock(b); e != nil {
			t.Fatal(e)
		}
	}
	send := func(p ed25519.PrivateKey, to string, amt, nonce uint64) {
		tx := &Tx{To: to, Amount: amt, Fee: 1000, Nonce: nonce}
		SignTxAt(tx, p, uint64(len(c.blocks)))
		if e := c.AddTx(tx); e != nil {
			t.Fatalf("AddTx: %v", e)
		}
	}
	send(privA, B, 100, 0) // block 1: A->B and B->A in the SAME block (A appears twice)
	send(privB, A, 50, 0)
	mine()
	send(privA, B, 10, 1) // block 2
	mine()
	mine() // block 3: coinbase only

	type q struct {
		addr     string
		lim, off int
	}
	qs := []q{{A, 50, 0}, {A, 1, 0}, {A, 1, 1}, {A, 2, 1}, {B, 50, 0}, {miner, 50, 0}}
	ref := map[q][]HistoryItem{}
	for _, x := range qs {
		ref[x] = c.History(x.addr, x.lim, x.off)
	}

	c2, err := OpenChain(dir, true, true) // import jsonl -> bbolt with rows
	if err != nil {
		t.Fatal(err)
	}
	defer c2.store.close()
	if c2.store == nil {
		t.Fatal("expected a bbolt store")
	}
	if v := c2.store.schema(); v != storeSchemaVersion {
		t.Fatalf("expected schema %d, got %d", storeSchemaVersion, v)
	}

	check := func(label string) {
		for _, x := range qs {
			got := c2.History(x.addr, x.lim, x.off)
			if !sameHist(got, ref[x]) {
				t.Fatalf("%s History(%s,lim%d,off%d) != jsonl\n got=%+v\n want=%+v",
					label, x.addr[:8], x.lim, x.off, got, ref[x])
			}
		}
	}

	// (1) row path == jsonl reference
	check("ROW")

	// (2) wipe all addrTx VALUES -> forces the block-load fallback; must still match.
	var keys [][]byte
	if err := c2.store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bkAddrTx).ForEach(func(k, _ []byte) error {
			keys = append(keys, append([]byte(nil), k...))
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := c2.store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bkAddrTx)
		for _, k := range keys {
			if e := b.Put(k, nil); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check("FALLBACK")

	// (3) backfillRows rebuilds the rows -> still matches.
	if err := c2.store.backfillRows(c2.blocks); err != nil {
		t.Fatal(err)
	}
	check("BACKFILL")
}
