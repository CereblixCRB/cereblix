package core

import (
	"crypto/ed25519"
	"testing"
)

// TestReplaceByFee verifies that a mempool tx at the same (sender, nonce) as a
// pending one replaces it only when it pays >= 10% more fee.
func TestReplaceByFee(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	from := AddrFromPub(pub)
	c.state[from] = &Account{Balance: 1_000_000}
	dst := "crb1" + "0000000000000000000000000000000000000000"
	h := uint64(len(c.blocks))

	mk := func(fee uint64) *Tx {
		tx := &Tx{To: dst, Amount: 100, Fee: fee, Nonce: 0}
		SignTxAt(tx, priv, h)
		return tx
	}

	orig := mk(5000)
	if err := c.AddTx(orig); err != nil {
		t.Fatalf("original tx rejected: %v", err)
	}
	// too-small bump (< +10%) -> rejected, original stays
	if err := c.AddTx(mk(5400)); err == nil {
		t.Fatal("replacement with <10% bump should be rejected")
	}
	// sufficient bump (>= +10%) -> replaces
	repl := mk(5500)
	if err := c.AddTx(repl); err != nil {
		t.Fatalf("valid replacement rejected: %v", err)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.mempool[orig.ID()]; ok {
		t.Fatal("original tx should have been evicted by the replacement")
	}
	if _, ok := c.mempool[repl.ID()]; !ok {
		t.Fatal("replacement tx should be in the mempool")
	}
	if len(c.mempool) != 1 {
		t.Fatalf("expected exactly 1 tx after replacement, got %d", len(c.mempool))
	}
}
