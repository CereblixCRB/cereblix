package core

import (
	"math/big"
	"strings"
	"testing"
)

// TestBboltChainEquivalenceAndPersist guards slice 2: a chain reopened on bbolt
// (migrating blocks.jsonl) is byte-identical to the jsonl chain (tip/cumwork/
// supply/state/history), and a block appended in bbolt mode survives a reopen.
func TestBboltChainEquivalenceAndPersist(t *testing.T) {
	addr := "crb1" + strings.Repeat("c", 40)
	dir := t.TempDir()

	// Build 8 blocks on the jsonl chain.
	c1, err := NewChain(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		b, err := c1.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate: %v", err)
		}
		c1.verifiedPow[b.Hash()] = true
		if err := c1.AddBlock(b); err != nil {
			t.Fatalf("AddBlock: %v", err)
		}
	}
	wantTip := c1.Tip().Hash()
	wantCum := new(big.Int).Set(c1.cumWork)
	wantSup := c1.supply
	wantStateLen := len(c1.state)
	wantHist := c1.History(addr, 50, 0)

	// Reopen the SAME dir on bbolt -> migrates blocks.jsonl -> bbolt, loads.
	c2, err := OpenChain(dir, true)
	if err != nil {
		t.Fatalf("OpenChain bbolt: %v", err)
	}
	if c2.store == nil {
		t.Fatal("expected a bbolt store")
	}
	if c2.Tip().Hash() != wantTip {
		t.Fatalf("tip: bbolt=%s jsonl=%s", c2.Tip().Hash(), wantTip)
	}
	if c2.cumWork.Cmp(wantCum) != 0 {
		t.Fatalf("cumwork mismatch: bbolt=%s jsonl=%s", c2.cumWork, wantCum)
	}
	if c2.supply != wantSup {
		t.Fatalf("supply: bbolt=%d jsonl=%d", c2.supply, wantSup)
	}
	if len(c2.state) != wantStateLen {
		t.Fatalf("state size: bbolt=%d jsonl=%d", len(c2.state), wantStateLen)
	}
	if !sameHist(c2.History(addr, 50, 0), wantHist) {
		t.Fatal("history mismatch after migration")
	}

	// Extend in bbolt mode, then reopen to confirm the write persisted.
	b, err := c2.BuildTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	c2.verifiedPow[b.Hash()] = true
	if err := c2.AddBlock(b); err != nil {
		t.Fatalf("AddBlock bbolt: %v", err)
	}
	extTip, extH := c2.Tip().Hash(), c2.Height()
	if err := c2.store.close(); err != nil {
		t.Fatal(err)
	}

	c3, err := OpenChain(dir, true)
	if err != nil {
		t.Fatalf("reopen bbolt: %v", err)
	}
	defer c3.store.close()
	if c3.Height() != extH || c3.Tip().Hash() != extTip {
		t.Fatalf("bbolt extend not persisted: reopened tip=%s h=%d want %s h=%d",
			c3.Tip().Hash(), c3.Height(), extTip, extH)
	}
}
