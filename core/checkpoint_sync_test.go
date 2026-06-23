package core

import (
	"strings"
	"testing"
)

// TestSyncAcrossCheckpointFromGenesis guards the checkpoint-guard fix: a fresh
// node at genesis that still holds a (non-empty) checkpoint — the "lost blocks,
// kept checkpoints.json" wedge — must be able to forward-sync ACROSS that
// checkpoint, while a candidate that conflicts with the checkpoint is still
// rejected.
func TestSyncAcrossCheckpointFromGenesis(t *testing.T) {
	addr := "crb1" + strings.Repeat("d", 40)

	ref, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var blocks []*Block
	for i := 0; i < 4; i++ {
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
	cpHash := blocks[1].Hash() // block at height 2

	// (1) Fresh node at genesis WITH the correct checkpoint at height 2 loaded must
	// forward-sync across it from height 1 (the old coarse guard wedged this).
	fresh, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fresh.Checkpoints[2] = cpHash
	for _, b := range blocks {
		fresh.verifiedPow[b.Hash()] = true
	}
	if err := fresh.TryAdoptChain(1, blocks); err != nil {
		t.Fatalf("fresh node holding a checkpoint must sync across it: %v", err)
	}
	if fresh.Tip().Hash() != ref.Tip().Hash() {
		t.Fatalf("did not sync to ref tip: got %s want %s", fresh.Tip().Hash(), ref.Tip().Hash())
	}

	// (2) A candidate whose block at the checkpoint height has the WRONG hash must
	// still be rejected (validateBlock enforces the checkpoint).
	bad, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad.Checkpoints[2] = strings.Repeat("0", 64) // != blocks[1].Hash()
	for _, b := range blocks {
		bad.verifiedPow[b.Hash()] = true
	}
	if err := bad.TryAdoptChain(1, blocks); err == nil {
		t.Fatal("candidate conflicting with the checkpoint must be rejected")
	}
}
