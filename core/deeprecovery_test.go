package core

import (
	"strings"
	"testing"
)

// drBuild appends n PoW-preseeded blocks to c (built by c's own BuildTemplate, so they
// signal the current consensus version v4) and returns them.
func drBuild(t *testing.T, c *Chain, addr string, n int) []*Block {
	t.Helper()
	var bs []*Block
	for i := 0; i < n; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate: %v", err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock: %v", err)
		}
		bs = append(bs, b)
	}
	return bs
}

// drLoad loads a base prefix (heights 1..) into a fresh chain via the depth-0 fast path.
func drLoad(t *testing.T, c *Chain, base []*Block) {
	t.Helper()
	for _, b := range base {
		c.verifiedPow[b.Hash()] = true
	}
	if err := c.TryAdoptChain(base[0].Height, base); err != nil {
		t.Fatalf("drLoad: %v", err)
	}
}

// deepReorgScenario builds: a chain `c` (base of baseN blocks + our 2-block extension)
// and a HEAVIER 4-block candidate forking at baseN (so adopting it is a depth-2 reorg
// that outweighs our chain). MaxReorgDepth=1, so the candidate exceeds the cap and must
// go through the gated deep-recovery branch. Returns (c, candidate).
func deepReorgScenario(t *testing.T, baseN int) (*Chain, []*Block) {
	t.Helper()
	addr := "crb1" + strings.Repeat("f", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.MaxReorgDepth = 1
	base := drBuild(t, c, addr, baseN) // c tip height = baseN
	drBuild(t, c, addr, 2)             // our +2 → tip baseN+2, len baseN+3

	fc, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fc.MaxReorgDepth = 1
	drLoad(t, fc, base)              // fork chain at height baseN
	cand := drBuild(t, fc, addr, 4) // heavier branch (4 blocks) forking at baseN
	for _, b := range cand {
		c.verifiedPow[b.Hash()] = true
	}
	return c, cand
}

// TestDeepRecoveryGatedAndAnchored is the hardfork-safety guard for the consensus-v4
// checkpoint-anchored deep-reorg recovery.
func TestDeepRecoveryGatedAndAnchored(t *testing.T) {
	// --- ACTIVE: a 100+ block chain (all v4-signaled) → deep-recovery is active. ---
	// (A) v4 ACTIVE, NO anchor → still rejected (guard intact).
	c, cand := deepReorgScenario(t, 102)
	if !deepRecoveryActiveAt(c.blocks, uint64(len(c.blocks))) {
		t.Fatal("precondition: deep-recovery should be ACTIVE on a 105-block v4 chain")
	}
	sh := cand[0].Height
	if err := c.TryAdoptChain(sh, cand); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("active+no-anchor: expected 'reorg too deep', got %v", err)
	}

	// (B) v4 ACTIVE, WRONG anchor → rejected.
	c, cand = deepReorgScenario(t, 102)
	top := cand[len(cand)-1]
	c.SetAuthorityAnchor(Checkpoint{Height: top.Height, Hash: strings.Repeat("0", 64)})
	if err := c.TryAdoptChain(cand[0].Height, cand); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("active+wrong-anchor: expected 'reorg too deep', got %v", err)
	}

	// (C) v4 ACTIVE, MATCHING signed anchor → accepted (autonomous deep recovery).
	c, cand = deepReorgScenario(t, 102)
	top = cand[len(cand)-1]
	c.SetAuthorityAnchor(Checkpoint{Height: top.Height, Hash: top.Hash()})
	if err := c.TryAdoptChain(cand[0].Height, cand); err != nil {
		t.Fatalf("active+matching-anchor: expected adopt, got %v", err)
	}
	if c.Tip().Hash() != top.Hash() {
		t.Fatal("active+matching-anchor: chain did not converge to the anchored candidate")
	}

	// --- INACTIVE: a short (<100) chain → deep-recovery NOT active → byte-identical to v3. ---
	// (D) v4 INACTIVE, MATCHING anchor → STILL rejected (the gate keeps the old 51% guard).
	c, cand = deepReorgScenario(t, 10)
	if deepRecoveryActiveAt(c.blocks, uint64(len(c.blocks))) {
		t.Fatal("precondition: deep-recovery must be INACTIVE on a 13-block chain")
	}
	top = cand[len(cand)-1]
	c.SetAuthorityAnchor(Checkpoint{Height: top.Height, Hash: top.Hash()})
	if err := c.TryAdoptChain(cand[0].Height, cand); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("inactive+matching-anchor: expected 'reorg too deep' (gate off), got %v", err)
	}
}

// TestCandidateMeetsAnchor checks the pure anchor-match predicate.
func TestCandidateMeetsAnchor(t *testing.T) {
	addr := "crb1" + strings.Repeat("a", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocks := drBuild(t, c, addr, 5) // heights 1..5
	start := blocks[0].Height        // 1
	target := blocks[3]              // height 4

	if !candidateMeetsAnchor(Checkpoint{Height: target.Height, Hash: target.Hash()}, start, blocks) {
		t.Fatal("matching anchor should be found in the candidate")
	}
	if candidateMeetsAnchor(Checkpoint{Height: target.Height, Hash: "deadbeef"}, start, blocks) {
		t.Fatal("wrong hash must not match")
	}
	if candidateMeetsAnchor(Checkpoint{Height: 0, Hash: target.Hash()}, start, blocks) {
		t.Fatal("zero-height anchor must not match")
	}
	if candidateMeetsAnchor(Checkpoint{Height: start - 1, Hash: target.Hash()}, start, blocks) {
		t.Fatal("anchor below the candidate start must not match")
	}
	if candidateMeetsAnchor(Checkpoint{Height: target.Height + 100, Hash: target.Hash()}, start, blocks) {
		t.Fatal("anchor above the candidate range must not match")
	}
}
