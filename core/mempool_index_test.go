package core

import (
	"crypto/ed25519"
	"testing"
)

// assertBySenderConsistent verifies the per-sender index is in exact lockstep
// with the mempool: same membership, correct owner, nonce-sorted.
func assertBySenderConsistent(t *testing.T, c *Chain) {
	t.Helper()
	total := 0
	for from, lst := range c.bySender {
		total += len(lst)
		for i, m := range lst {
			if _, ok := c.mempool[m.ID()]; !ok {
				t.Fatalf("bySender[%s] holds %s not in mempool", from[:10], m.ID()[:8])
			}
			if mf, _ := m.FromAddr(); mf != from {
				t.Fatalf("bySender[%s] holds a tx whose sender is %s", from[:10], mf[:10])
			}
			if i > 0 && lst[i-1].Nonce > m.Nonce {
				t.Fatalf("bySender[%s] not nonce-sorted (%d before %d)", from[:10], lst[i-1].Nonce, m.Nonce)
			}
		}
	}
	if total != len(c.mempool) {
		t.Fatalf("index size %d != mempool size %d", total, len(c.mempool))
	}
	for _, m := range c.mempool {
		from, err := m.FromAddr()
		if err != nil {
			continue
		}
		found := false
		for _, x := range c.bySender[from] {
			if x.ID() == m.ID() {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("mempool tx %s missing from bySender[%s]", m.ID()[:8], from[:10])
		}
	}
}

func TestBySenderStaysConsistent(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)
	a, b := AddrFromPub(pubA), AddrFromPub(pubB)
	c.state[a] = &Account{Balance: 10_000_000}
	c.state[b] = &Account{Balance: 10_000_000}
	h := uint64(len(c.blocks))
	dst := "crb1" + "0000000000000000000000000000000000000000"
	mk := func(priv ed25519.PrivateKey, nonce, fee uint64) *Tx {
		tx := &Tx{To: dst, Amount: 100, Fee: fee, Nonce: nonce}
		SignTxAt(tx, priv, h)
		return tx
	}
	for _, tx := range []*Tx{
		mk(privA, 0, 2000), mk(privB, 0, 2000), mk(privA, 1, 2000),
		mk(privB, 1, 2000), mk(privA, 2, 2000),
	} {
		if err := c.AddTx(tx); err != nil {
			t.Fatalf("AddTx: %v", err)
		}
		assertBySenderConsistent(t, c)
	}
	// replace-by-fee on A nonce 1
	if err := c.AddTx(mk(privA, 1, 5000)); err != nil {
		t.Fatalf("RBF: %v", err)
	}
	assertBySenderConsistent(t, c)
	// prune must keep the index consistent
	c.mu.Lock()
	c.pruneMempoolLocked()
	c.mu.Unlock()
	assertBySenderConsistent(t, c)
}
