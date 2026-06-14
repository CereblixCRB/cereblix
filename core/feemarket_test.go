package core

import (
	"crypto/ed25519"
	"testing"
)

// TestFeeMarketSelection verifies the Bitcoin-style block builder:
//   - higher-fee txns are included first (across senders),
//   - a sender's txns stay in nonce order,
//   - and a block is NEVER empty while the mempool holds includable txns.
func TestFeeMarketSelection(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mkKey := func() (ed25519.PrivateKey, string) {
		pub, priv, _ := ed25519.GenerateKey(nil)
		return priv, AddrFromPub(pub)
	}
	pa, A := mkKey()
	pb, B := mkKey()
	pc, C := mkKey()
	dst := "crb1" + "0000000000000000000000000000000000000000"

	for _, a := range []string{A, B, C} {
		c.state[a] = &Account{Balance: 1_000_000}
	}
	h := uint64(len(c.blocks)) // template height (= 1, just genesis present)
	mk := func(priv ed25519.PrivateKey, nonce, fee uint64) {
		tx := &Tx{To: dst, Amount: 100, Fee: fee, Nonce: nonce}
		SignTxAt(tx, priv, h)
		c.mempool[tx.ID()] = tx
	}
	mk(pa, 0, 5000) // A #0
	mk(pa, 1, 5000) // A #1
	mk(pb, 0, 9000) // B #0  (highest fee)
	mk(pc, 0, 2000) // C #0  (lowest fee)

	blk, err := c.BuildTemplate(dst)
	if err != nil {
		t.Fatal(err)
	}
	// The built block must advertise this node's consensus version in its coinbase
	// (drives readiness-gated upgrades).
	if v := coinbaseVersion(blk); v != NodeConsensusVersion {
		t.Fatalf("built block signals version %d, want %d", v, NodeConsensusVersion)
	}
	got := blk.Txs[1:] // drop coinbase
	if len(got) == 0 {
		t.Fatal("EMPTY block while mempool has includable txns")
	}
	if len(got) != 4 {
		t.Fatalf("expected all 4 txns picked, got %d", len(got))
	}
	wantFrom := []string{B, A, A, C}
	wantNonce := []uint64{0, 0, 1, 0}
	for i, tx := range got {
		from, _ := tx.FromAddr()
		if from != wantFrom[i] || tx.Nonce != wantNonce[i] {
			t.Fatalf("pos %d: got (%s.., n%d), want (%s.., n%d) - fee priority / nonce order broken",
				i, from[:10], tx.Nonce, wantFrom[i][:10], wantNonce[i])
		}
	}
}

// TestSuggestFee verifies the wallet fee estimator: clear network -> floor,
// congested -> just over the next-block cut, and sub-floor txns are ignored.
func TestSuggestFee(t *testing.T) {
	const floor = 1000
	capN := MaxBlockTxs - 1
	rep := func(v uint64, n int) []uint64 {
		s := make([]uint64, n)
		for i := range s {
			s[i] = v
		}
		return s
	}

	if got := suggestFee(nil, floor); got != floor {
		t.Fatalf("empty mempool: got %d, want floor %d", got, floor)
	}
	// One block fits everything -> floor, even if fees are high.
	if got := suggestFee(rep(9000, capN), floor); got != floor {
		t.Fatalf("uncongested: got %d, want floor %d", got, floor)
	}
	// Sub-floor txns don't count toward congestion.
	below := append(rep(9000, capN), rep(500, 100)...)
	if got := suggestFee(below, floor); got != floor {
		t.Fatalf("sub-floor ignored: got %d, want floor %d", got, floor)
	}
	// Congested: cap txns @5000 + extra cheaper ones -> cut=5000, +12.5%.
	busy := append(rep(5000, capN), rep(2000, 60)...)
	if got := suggestFee(busy, floor); got != 5000+5000/8 {
		t.Fatalf("congested: got %d, want %d", got, 5000+5000/8)
	}
}
