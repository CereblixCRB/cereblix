package core

import (
	"fmt"
	"testing"
)

// TestVerifiedPowBounded guards the verifiedPow leak fix: the PoW cache must not
// grow without bound.
func TestVerifiedPowBounded(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxVerifiedPow*3; i++ {
		c.markPowVerified(fmt.Sprintf("hash-%064d", i))
	}
	if len(c.verifiedPow) > maxVerifiedPow {
		t.Fatalf("verifiedPow grew unbounded: %d > cap %d", len(c.verifiedPow), maxVerifiedPow)
	}
}
