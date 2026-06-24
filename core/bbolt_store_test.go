package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestBboltMigrationMatchesChain guards slice 1 of the 2.3.0 storage migration:
// importing blocks.jsonl into the bbolt store must reproduce the chain exactly
// (block hashes, tip, cumwork), the tx index must resolve, and a re-migration
// over a populated store must be refused.
func TestBboltMigrationMatchesChain(t *testing.T) {
	addr := "crb1" + strings.Repeat("b", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate %d: %v", i, err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock %d: %v", i, err)
		}
	}

	st, err := openBlockStore(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	n, err := st.migrateFromJSONL(c.blocksFile())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n != len(c.blocks) {
		t.Fatalf("migrated %d blocks, chain has %d", n, len(c.blocks))
	}

	tip, ok := st.tipHeight()
	if !ok || tip != uint64(len(c.blocks)-1) {
		t.Fatalf("store tip %d (ok=%v), want %d", tip, ok, len(c.blocks)-1)
	}
	if st.cumWork().Cmp(c.cumWork) != 0 {
		t.Fatalf("cumwork mismatch: store=%s chain=%s", st.cumWork(), c.cumWork)
	}
	for h := uint64(0); h <= tip; h++ {
		b, err := st.getBlock(h)
		if err != nil {
			t.Fatalf("getBlock %d: %v", h, err)
		}
		if b.Hash() != c.blocks[h].Hash() {
			t.Fatalf("block %d hash mismatch: store=%s chain=%s", h, b.Hash(), c.blocks[h].Hash())
		}
		if b.Height != c.blocks[h].Height || b.PrevHash != c.blocks[h].PrevHash {
			t.Fatalf("block %d header mismatch", h)
		}
	}

	// tx index resolves block 1's coinbase to (height 1, idx 0)
	cbID := c.blocks[1].Txs[0].ID()
	if hh, idx, ok := st.findTxLoc(cbID); !ok || hh != 1 || idx != 0 {
		t.Fatalf("findTxLoc(coinbase@1)=%d,%d,%v want 1,0,true", hh, idx, ok)
	}

	// idempotency: re-migrating over a populated store must fail
	if _, err := st.migrateFromJSONL(c.blocksFile()); err == nil {
		t.Fatal("re-migration over a populated store should have failed")
	}
}
