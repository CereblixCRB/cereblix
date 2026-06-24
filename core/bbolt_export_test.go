package core

import (
	"strings"
	"testing"
)

// TestBboltExportRoundtrip guards the rollback path: exporting a bbolt store back
// to blocks.jsonl yields a chain the legacy jsonl binary loads identically.
func TestBboltExportRoundtrip(t *testing.T) {
	addr := "crb1" + strings.Repeat("d", 40)
	dir := t.TempDir()

	c, err := OpenChain(dir, true) // fresh bbolt
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatal(err)
		}
	}
	wantTip, wantH := c.Tip().Hash(), c.Height()
	if err := c.store.close(); err != nil {
		t.Fatal(err)
	}

	n, err := ExportBoltToJSONL(dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != int(wantH)+1 {
		t.Fatalf("exported %d blocks, want %d", n, wantH+1)
	}

	// Legacy jsonl binary reopens the exported file — must match exactly.
	c2, err := NewChain(dir)
	if err != nil {
		t.Fatalf("jsonl reopen after export: %v", err)
	}
	if c2.Height() != wantH || c2.Tip().Hash() != wantTip {
		t.Fatalf("rollback mismatch: jsonl tip=%s h=%d want %s h=%d",
			c2.Tip().Hash(), c2.Height(), wantTip, wantH)
	}
}
