package main

import (
	"path/filepath"
	"testing"
	"time"

	"cereblix/core"
)

func freshState() {
	st = &state{
		Shares: map[string]float64{}, Earned: map[string]uint64{},
		InFlight: map[string]*inflight{}, Owed: map[string]uint64{}, Paid: map[string]uint64{},
	}
}

// PPLNS window must trim to ~pplnsN and the cached sum must equal the recomputed sum.
func TestPPLNSWindowTrim(t *testing.T) {
	freshState()
	pplnsN = 10
	st.mu.Lock()
	for i := 0; i < 25; i++ {
		addPPLNS("crb1a", 1)
	}
	st.mu.Unlock()
	if st.PplnsSum > pplnsN+1 || st.PplnsSum < pplnsN-1 {
		t.Fatalf("window not trimmed near N: sum=%v want ~%v", st.PplnsSum, pplnsN)
	}
	var s float64
	for _, e := range st.PPLNS {
		s += e.W
	}
	if s != st.PplnsSum {
		t.Fatalf("PplnsSum cache %v != recomputed %v", st.PplnsSum, s)
	}
}

// onBlockFound pays proportional to PPLNS weight, excludes poolAddr from payout (but keeps it
// in the denominator), and does NOT clear the window (PPLNS slides).
func TestPPLNSPayout(t *testing.T) {
	freshState()
	pplnsN = 1000
	poolAddr = "crb1pool"
	feePermil = 10 // 1% fee
	statePath = filepath.Join(t.TempDir(), "pool.json")

	st.mu.Lock()
	addPPLNS("crb1a", 30)
	addPPLNS("crb1b", 10)
	addPPLNS(poolAddr, 60) // operator's own share: counted in denominator, never paid
	st.mu.Unlock()

	const h = 10000
	reward := core.BlockSubsidy(h)
	pot := reward - reward*feePermil/1000
	onBlockFound(h)

	wantA := uint64(float64(pot) * 30.0 / 100.0)
	wantB := uint64(float64(pot) * 10.0 / 100.0)
	if st.Earned["crb1a"] != wantA {
		t.Fatalf("crb1a earned %d, want %d", st.Earned["crb1a"], wantA)
	}
	if st.Earned["crb1b"] != wantB {
		t.Fatalf("crb1b earned %d, want %d", st.Earned["crb1b"], wantB)
	}
	if st.Earned[poolAddr] != 0 {
		t.Fatalf("poolAddr must not be paid from shares, got %d", st.Earned[poolAddr])
	}
	if a, b := st.Earned["crb1a"], st.Earned["crb1b"]; a != 3*b {
		t.Fatalf("ratio must be 3:1, got %d:%d", a, b)
	}
	// window must persist (slides, not cleared) so subsequent blocks keep paying recent shares
	if len(st.PPLNS) != 3 {
		t.Fatalf("PPLNS window must NOT be cleared on block, len=%d", len(st.PPLNS))
	}

	// a second block keeps paying the same window (this is the hop-proof property)
	onBlockFound(h)
	if st.Earned["crb1a"] != 2*wantA {
		t.Fatalf("second block should pay window again: crb1a=%d want %d", st.Earned["crb1a"], 2*wantA)
	}
}

// anti-flood: a single address's burst is bounded by its bucket (no time advance = no refill).
func TestSubmitAntiFlood(t *testing.T) {
	perAddrRate, perAddrBurst = 1000, 600
	addrBuckets = map[string]*tbucket{}
	addrSeen = map[string]time.Time{}
	now := time.Unix(1_000_000, 0)
	ok := 0
	for i := 0; i < int(perAddrBurst)+200; i++ {
		if submitAllowed("crb1spammer", now) {
			ok++
		}
	}
	if ok > int(perAddrBurst)+1 {
		t.Fatalf("per-address burst not enforced: allowed %d, cap ~%d", ok, int(perAddrBurst))
	}
	if ok < int(perAddrBurst)-1 {
		t.Fatalf("legit burst should pass ~burst, got %d", ok)
	}
}
