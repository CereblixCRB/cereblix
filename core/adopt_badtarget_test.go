package core

import (
	"strings"
	"testing"
)

// TestAdoptRejectsMalformedTargetWithoutPanic guards H1: the header-only work
// pre-check in TryAdoptChain must reject a candidate whose Target is undecodable
// with an error, NOT panic via WorkOf(nil). A peer could otherwise stall sync by
// offering one malformed block.
func TestAdoptRejectsMalformedTargetWithoutPanic(t *testing.T) {
	addr := "crb1" + strings.Repeat("e", 40)
	ref, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ref.BuildTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	b.Target = "zz" // undecodable -> TargetInt returns (nil, err)

	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TryAdoptChain panicked on malformed target: %v", r)
		}
	}()
	if err := c.TryAdoptChain(1, []*Block{b}); err == nil {
		t.Fatal("expected error for malformed candidate target, got nil")
	}
}
