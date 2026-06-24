package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBboltAutoFallbackToJSONL guards the safety net: if the bbolt DB can't be
// opened, the node must auto-fall-back to blocks.jsonl (never brick).
func TestBboltAutoFallbackToJSONL(t *testing.T) {
	addr := "crb1" + strings.Repeat("f", 40)
	dir := t.TempDir()

	c1, err := NewChain(dir) // jsonl
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		b, e := c1.BuildTemplate(addr)
		if e != nil {
			t.Fatal(e)
		}
		c1.verifiedPow[b.Hash()] = true
		if e := c1.AddBlock(b); e != nil {
			t.Fatal(e)
		}
	}
	wantTip, wantH := c1.Tip().Hash(), c1.Height()

	// Corrupt chain.db so bbolt can't open it.
	if e := os.WriteFile(filepath.Join(dir, "chain.db"), []byte("this is not a bbolt database"), 0o600); e != nil {
		t.Fatal(e)
	}

	// Opening in bbolt mode must NOT error or brick — it falls back to jsonl.
	c2, err := OpenChain(dir, true, true)
	if err != nil {
		t.Fatalf("auto-fallback should not error: %v", err)
	}
	if c2.UsingBolt() {
		t.Fatal("expected auto-fallback to jsonl, but UsingBolt() is true")
	}
	if c2.Height() != wantH || c2.Tip().Hash() != wantTip {
		t.Fatalf("jsonl fallback mismatch: tip=%s h=%d want %s h=%d",
			c2.Tip().Hash(), c2.Height(), wantTip, wantH)
	}
}
