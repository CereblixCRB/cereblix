package core

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// TestHashCacheCorrectAndNoStale guards #3 (immutable hash caching): the cache
// must (a) match a fresh compute, (b) survive JSON round-trip without changing the
// wire format, and (c) NEVER go stale on a mutable/template block — the key risk,
// since the miner mutates a fresh block's nonce.
func TestHashCacheCorrectAndNoStale(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	tx := &Tx{To: "crb1" + strings.Repeat("a", 40), Amount: 5, Fee: 1000, Nonce: 0}
	SignTxAt(tx, priv, 1)
	if tx.id == "" || tx.id != tx.computeID() {
		t.Fatal("SignTxAt did not cache the correct id")
	}
	raw, _ := json.Marshal(tx)
	var tx2 Tx
	if err := json.Unmarshal(raw, &tx2); err != nil {
		t.Fatal(err)
	}
	if tx2.id == "" || tx2.ID() != tx.ID() {
		t.Fatalf("round-trip id mismatch: %s vs %s", tx2.ID(), tx.ID())
	}
	if raw2, _ := json.Marshal(&tx2); string(raw2) != string(raw) {
		t.Fatalf("tx wire format changed by cache:\n%s\n%s", raw, raw2)
	}

	// Fresh (template-like) block: hash must compute on demand and NOT be cached,
	// so a nonce change yields a new, correct hash.
	b := &Block{Version: 1, Height: 1, Time: 100, PrevHash: strings.Repeat("0", 64),
		TxRoot: ComputeTxRoot([]*Tx{tx}), Target: strings.Repeat("f", 64), Nonce: 0, Txs: []*Tx{tx}}
	if b.hash != "" {
		t.Fatal("fresh block should not be pre-cached")
	}
	h0 := b.Hash()
	b.Nonce = 42
	h1 := b.Hash()
	if h0 == h1 {
		t.Fatal("STALE CACHE: hash unchanged after nonce mutation")
	}
	if h1 != b.computeHash() {
		t.Fatal("post-mutation hash incorrect")
	}

	// Final block round-trips: unmarshaled hash caches + matches, wire unchanged.
	rb, _ := json.Marshal(b)
	var b2 Block
	if err := json.Unmarshal(rb, &b2); err != nil {
		t.Fatal(err)
	}
	if b2.hash == "" || b2.Hash() != b.Hash() {
		t.Fatalf("round-trip block hash mismatch: %s vs %s", b2.Hash(), b.Hash())
	}
	if rb2, _ := json.Marshal(&b2); string(rb2) != string(rb) {
		t.Fatal("block wire format changed by cache")
	}
}
