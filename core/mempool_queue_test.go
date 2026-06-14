package core

import (
	"crypto/ed25519"
	"testing"
)

// TestMempoolQueuesFutureNonce verifies the Ethereum-style queue: a tx whose
// nonce runs ahead of the sender's next executable nonce is held (not rejected),
// and once the gap is filled BuildTemplate mines the whole run in nonce order.
func TestMempoolQueuesFutureNonce(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	from := AddrFromPub(pub)
	c.state[from] = &Account{Balance: 1_000_000}
	dst := "crb1" + "0000000000000000000000000000000000000000"
	h := uint64(len(c.blocks))
	mk := func(nonce, fee uint64) *Tx {
		tx := &Tx{To: dst, Amount: 100, Fee: fee, Nonce: nonce}
		SignTxAt(tx, priv, h)
		return tx
	}

	// Submit nonce 2 FIRST (account is at 0) - a gap of two ahead. The old
	// strict mempool rejected this ("bad nonce"); now it must be queued.
	if err := c.AddTx(mk(2, 1000)); err != nil {
		t.Fatalf("future-nonce tx (2) should be queued, got: %v", err)
	}
	// nonce 1 - still ahead of the executable nonce (0), queued.
	if err := c.AddTx(mk(1, 1000)); err != nil {
		t.Fatalf("future-nonce tx (1) should be queued, got: %v", err)
	}
	// nonce 0 - executable now, fills the gap.
	if err := c.AddTx(mk(0, 1000)); err != nil {
		t.Fatalf("executable tx (0) rejected: %v", err)
	}
	c.mu.RLock()
	n := len(c.mempool)
	c.mu.RUnlock()
	if n != 3 {
		t.Fatalf("expected 3 txns held in mempool, got %d", n)
	}

	// With the gap filled, a block template must contain all three in nonce
	// order (BuildTemplate only ever mines the contiguous run).
	tmpl, err := c.BuildTemplate(dst)
	if err != nil {
		t.Fatalf("BuildTemplate: %v", err)
	}
	var got []uint64
	for _, tx := range tmpl.Txs {
		if tx.IsCoinbase() {
			continue
		}
		got = append(got, tx.Nonce)
	}
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("expected nonces [0 1 2] mined in order, got %v", got)
	}
}

// TestMempoolNonceBounds verifies the queue's two guards: an already-used
// (too-low) nonce and one beyond MaxMempoolNonceGap are both rejected, while
// the next executable nonce is accepted.
func TestMempoolNonceBounds(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	from := AddrFromPub(pub)
	c.state[from] = &Account{Balance: 1_000_000, Nonce: 5}
	dst := "crb1" + "0000000000000000000000000000000000000000"
	h := uint64(len(c.blocks))
	mk := func(nonce uint64) *Tx {
		tx := &Tx{To: dst, Amount: 100, Fee: 1000, Nonce: nonce}
		SignTxAt(tx, priv, h)
		return tx
	}

	// Below the account nonce (5) - already spent on-chain - reject.
	if err := c.AddTx(mk(4)); err == nil {
		t.Fatal("stale (too-low) nonce should be rejected")
	}
	// Beyond the gap bound - reject.
	if err := c.AddTx(mk(5 + MaxMempoolNonceGap + 1)); err == nil {
		t.Fatal("nonce beyond MaxMempoolNonceGap should be rejected")
	}
	// Exactly the next executable nonce - accept.
	if err := c.AddTx(mk(5)); err != nil {
		t.Fatalf("executable nonce rejected: %v", err)
	}
}
