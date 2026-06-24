package core

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

// TestBboltServesNavigationLikeJSONL guards slice 4: with bbolt, History/FindTx/
// BlockByHash are served from the DB index and must be byte-identical to the jsonl
// in-memory path — including an address with MULTIPLE txs in one block (within-block
// ordering) and offset paging — and the in-memory index must NOT be built.
func TestBboltServesNavigationLikeJSONL(t *testing.T) {
	dir := t.TempDir()
	c, err := NewChain(dir) // jsonl
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

	someTxID := c.blocks[1].Txs[1].ID()
	someBlockHash := c.blocks[2].Hash()

	type q struct {
		addr     string
		lim, off int
	}
	qs := []q{{A, 50, 0}, {A, 1, 0}, {A, 1, 1}, {A, 2, 1}, {B, 50, 0}, {miner, 50, 0}}
	want := map[q][]HistoryItem{}
	for _, x := range qs {
		want[x] = c.History(x.addr, x.lim, x.off)
	}
	wantFind := c.FindTx(someTxID)

	c2, err := OpenChain(dir, true, true) // import jsonl; then serves navigation from the DB
	if err != nil {
		t.Fatal(err)
	}
	defer c2.store.close()
	if c2.store == nil {
		t.Fatal("expected a bbolt store")
	}
	for _, x := range qs {
		got := c2.History(x.addr, x.lim, x.off)
		if !sameHist(got, want[x]) {
			t.Fatalf("History(%s,lim%d,off%d): bbolt != jsonl\n bbolt=%+v\n jsonl=%+v",
				x.addr[:8], x.lim, x.off, got, want[x])
		}
	}
	if got := c2.FindTx(someTxID); got != wantFind {
		t.Fatalf("FindTx: bbolt=%+v jsonl=%+v", got, wantFind)
	}
	if b := c2.BlockByHash(someBlockHash); b == nil || b.Hash() != someBlockHash {
		t.Fatalf("bbolt BlockByHash mismatch for %s", someBlockHash)
	}
	if len(c2.addrTx) != 0 {
		t.Fatalf("bbolt mode must not build the in-memory addrTx, got %d entries", len(c2.addrTx))
	}
}
