package core

import (
	"strings"
	"testing"
)

// TestFastPathExtensionMatchesReplay guards the Phase A refactor: the depth-0
// fast path in TryAdoptChain must produce byte-identical state to the proven
// block-by-block AddBlock path (which is itself a genesis replay). PoW is
// pre-seeded via verifiedPow so the test needn't mine.
func TestFastPathExtensionMatchesReplay(t *testing.T) {
	addr := "crb1" + strings.Repeat("c", 40)

	// Reference chain: build 5 blocks one-by-one via AddBlock.
	ref, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var blocks []*Block
	for i := 0; i < 5; i++ {
		b, err := ref.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate %d: %v", i, err)
		}
		ref.verifiedPow[b.Hash()] = true
		if err := ref.AddBlock(b); err != nil {
			t.Fatalf("AddBlock %d: %v", i, err)
		}
		blocks = append(blocks, b)
	}

	// Fast-path chain: adopt the SAME blocks at once via TryAdoptChain (depth 0).
	fp, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		fp.verifiedPow[b.Hash()] = true
	}
	if err := fp.TryAdoptChain(1, blocks); err != nil {
		t.Fatalf("fast-path TryAdoptChain: %v", err)
	}

	// Everything that defines chain state must match byte-for-byte.
	if fp.Tip().Hash() != ref.Tip().Hash() {
		t.Fatalf("tip mismatch: fp=%s ref=%s", fp.Tip().Hash(), ref.Tip().Hash())
	}
	if fp.cumWork.Cmp(ref.cumWork) != 0 {
		t.Fatalf("cumwork mismatch: fp=%s ref=%s", fp.cumWork, ref.cumWork)
	}
	if fp.supply != ref.supply {
		t.Fatalf("supply mismatch: fp=%d ref=%d", fp.supply, ref.supply)
	}
	if len(fp.state) != len(ref.state) {
		t.Fatalf("state size mismatch: fp=%d ref=%d", len(fp.state), len(ref.state))
	}
	for k, v := range ref.state {
		got := fp.state[k]
		if got == nil || *got != *v {
			t.Fatalf("state mismatch at %s: fp=%+v ref=%+v", k, got, v)
		}
	}
	if len(fp.totals) != len(ref.totals) {
		t.Fatalf("totals size mismatch: fp=%d ref=%d", len(fp.totals), len(ref.totals))
	}
	for k, v := range ref.totals {
		got := fp.totals[k]
		if got == nil || *got != *v {
			t.Fatalf("totals mismatch at %s: fp=%+v ref=%+v", k, got, v)
		}
	}
}
