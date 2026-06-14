package core

import (
	"crypto/ed25519"
	"testing"
)

// TestChainIDSigningGate verifies the height-gated ChainID binding: a signature
// is valid only for the payload version matching the height it is applied at.
func TestChainIDSigningGate(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const dst = "crb1" + "0000000000000000000000000000000000000000"
	mk := func(height uint64) *Tx {
		tx := &Tx{To: dst, Amount: 100, Fee: 10, Nonce: 0}
		SignTxAt(tx, priv, height)
		return tx
	}

	// Signed below activation (v1 payload).
	low := mk(ChainIDHeight - 1)
	if err := low.CheckSigAt(ChainIDHeight - 1); err != nil {
		t.Fatalf("v1 tx should verify below activation: %v", err)
	}
	if err := low.CheckSigAt(ChainIDHeight); err == nil {
		t.Fatal("v1 tx must NOT verify at/after activation")
	}

	// Signed at activation (v2 payload, ChainID-bound).
	hi := mk(ChainIDHeight)
	if err := hi.CheckSigAt(ChainIDHeight); err != nil {
		t.Fatalf("v2 tx should verify at activation: %v", err)
	}
	if err := hi.CheckSigAt(ChainIDHeight - 1); err == nil {
		t.Fatal("v2 tx must NOT verify below activation")
	}

	// Legacy CheckSig stays equivalent to the pre-activation payload.
	if err := low.CheckSig(); err != nil {
		t.Fatalf("CheckSig should accept a v1 tx: %v", err)
	}
	if ChainID != GenesisBlock().Hash() {
		t.Fatal("ChainID must equal the genesis block hash")
	}
	if ChainID == "" {
		t.Fatal("ChainID must be set")
	}
}
