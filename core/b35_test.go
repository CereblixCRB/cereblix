package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// TestPreVerifySigsHeightKeyed guards the off-lock tx-signature pre-verification:
// a VALID sig is memoized under (txID, height); the memo is HEIGHT-keyed (a different
// height misses, since the ChainID-bound signing payload differs); and an INVALID
// signature is never recorded — so the locked validateTxAgainstState can only skip a
// genuinely-verified sig, never launder a bad one.
func TestPreVerifySigsHeightKeyed(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	to := "crb1" + strings.Repeat("a", 40)
	good := &Tx{To: to, Amount: 100, Fee: 1000, Nonce: 0}
	SignTxAt(good, priv, 5)
	bad := &Tx{To: to, Amount: 100, Fee: 1000, Nonce: 1,
		FromPub: hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		Sig:     hex.EncodeToString(make([]byte, ed25519.SignatureSize))} // zero sig

	c.PreVerifySigs(5, []*Block{{Height: 5, Txs: []*Tx{good, bad}}})

	if !c.sigPreVerified(good.ID(), 5) {
		t.Fatal("valid sig at height 5 must be pre-verified")
	}
	if c.sigPreVerified(good.ID(), 6) {
		t.Fatal("pre-verify must be HEIGHT-keyed: height 6 must miss")
	}
	if c.sigPreVerified(bad.ID(), 5) {
		t.Fatal("an invalid signature must NOT be recorded as verified")
	}
}

// TestAuthAnchorPersistAndForgeryReject: SetAuthorityAnchor persists to disk, and a
// restart re-checks the signature on load so an unsigned/forged on-disk anchor is
// rejected (it must never grant deep-reorg recovery).
func TestAuthAnchorPersistAndForgeryReject(t *testing.T) {
	dir := t.TempDir()
	c, err := NewChain(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.SetAuthorityAnchor(Checkpoint{Height: 50, Hash: strings.Repeat("a", 64), Sig: "00"})
	if c.authAnchor.Height != 50 {
		t.Fatal("SetAuthorityAnchor should store the anchor in memory (caller verifies the sig)")
	}
	c2, err := NewChain(dir) // reload from disk
	if err != nil {
		t.Fatal(err)
	}
	if c2.authAnchor.Height != 0 {
		t.Fatal("loadAuthAnchor must REJECT an unsigned/forged on-disk anchor")
	}
}

// TestConsensusStatusSignalCount: a chain built entirely by this (v4) binary signals
// v4 in every coinbase, so once past SignalWindow the reported v4 signal is a
// supermajority — the telemetry an operator watches climb toward activation.
func TestConsensusStatusSignalCount(t *testing.T) {
	addr := "crb1" + strings.Repeat("c", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DeepRecoveryWindow+5; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatal(err)
		}
	}
	sig, req, win, _ := c.ConsensusStatus()
	if win != DeepRecoveryWindow {
		t.Fatalf("window = %d, want %d", win, DeepRecoveryWindow)
	}
	if req != deepRecoveryRequired() {
		t.Fatalf("required = %d, want %d", req, deepRecoveryRequired())
	}
	if sig < req {
		t.Fatalf("a v4-built chain should signal >= required, got %d/%d over %d", sig, req, win)
	}
}
