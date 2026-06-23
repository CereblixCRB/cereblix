package core

import (
	"strings"
	"testing"
)

// TestActivationCacheByteIdentical guards #2: the cached activation path produces
// byte-identical difficulty AND fee-floor to the free (re-scanning) functions at
// every prefix, and the cache is NOT populated while the chain is pre-activation /
// not yet buried below the reorg horizon.
func TestActivationCacheByteIdentical(t *testing.T) {
	addr := "crb1" + strings.Repeat("d", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate %d: %v", i, err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock %d: %v", i, err)
		}
		if got, want := c.expectedTargetC(c.blocks), expectedTarget(c.blocks); got.Cmp(want) != 0 {
			t.Fatalf("h%d: expectedTargetC=%s != expectedTarget=%s", i, got, want)
		}
		if got, want := c.minFeeForC(c.blocks), minFeeFor(c.blocks); got != want {
			t.Fatalf("h%d: minFeeForC=%d != minFeeFor=%d", i, got, want)
		}
		if got, want := c.FeeFloor(), minFeeFor(c.blocks); got != want {
			t.Fatalf("h%d: FeeFloor=%d != minFeeFor=%d", i, got, want)
		}
	}
	// Pre-activation, short chain: nothing is buried below the reorg horizon, so the
	// sticky cache must stay empty (correctness guard against premature caching).
	if c.lwmaAct != 0 || c.feeAct != 0 {
		t.Fatalf("activation cached prematurely: lwmaAct=%d feeAct=%d", c.lwmaAct, c.feeAct)
	}
}
