package main

import (
	"encoding/json"
	"testing"
)

// computeBlockCredits is the SINGLE source of the per-block split used by BOTH db-mode and
// pool.json mode (db-mode persists the result via dbCreditBlock; pool.json adds to st.Earned).
// It must split the pot proportionally to PPLNS weight, count poolAddr in the denominator but
// never credit it, and return nothing for an empty/all-pool window.
func TestComputeBlockCredits(t *testing.T) {
	pool := "crb1pool"
	pot := uint64(1_000_000)
	win := []pplnsEntry{{"crb1a", 30}, {"crb1b", 10}, {pool, 60}} // total weight 100

	c := computeBlockCredits(win, pot, pool)
	if c[pool] != 0 {
		t.Fatalf("poolAddr must never be credited, got %d", c[pool])
	}
	wantA := uint64(float64(pot) * 30.0 / 100.0) // poolAddr's 60 stays in the denominator
	wantB := uint64(float64(pot) * 10.0 / 100.0)
	if c["crb1a"] != wantA {
		t.Fatalf("crb1a credited %d, want %d", c["crb1a"], wantA)
	}
	if c["crb1b"] != wantB {
		t.Fatalf("crb1b credited %d, want %d", c["crb1b"], wantB)
	}
	if c["crb1a"] != 3*c["crb1b"] {
		t.Fatalf("ratio must be 3:1, got %d:%d", c["crb1a"], c["crb1b"])
	}
	if got := computeBlockCredits(nil, pot, pool); len(got) != 0 {
		t.Fatalf("empty window must credit nobody, got %v", got)
	}
	if got := computeBlockCredits([]pplnsEntry{{pool, 5}}, pot, pool); len(got) != 0 {
		t.Fatalf("all-pool window must credit nobody, got %v", got)
	}
}

// The PPLNS snapshot is just json.Marshal(st.PPLNS); loadPPLNSSnapshot unmarshals it back and
// recomputes the sum. The on-wire keys (m/w) must match pplnsEntry AND the migrate script's seed
// row, or a promoted/restarted node would resume on a corrupt window. Round-trip must be exact.
func TestPPLNSSnapshotRoundTrip(t *testing.T) {
	freshState()
	pplnsN = 1000
	st.mu.Lock()
	addPPLNS("crb1a", 3)
	addPPLNS("crb1b", 2.5)
	addPPLNS("crb1a", 1)
	want := st.PplnsSum
	raw, err := json.Marshal(st.PPLNS)
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what loadPPLNSSnapshot does with the JSON read back from Postgres.
	var win []pplnsEntry
	if err := json.Unmarshal(raw, &win); err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, e := range win {
		sum += e.W
	}
	if sum != want {
		t.Fatalf("restored sum %v != original %v", sum, want)
	}
	if len(win) != 3 || win[0].M != "crb1a" || win[0].W != 3 || win[1].M != "crb1b" || win[1].W != 2.5 {
		t.Fatalf("window did not round-trip: %+v", win)
	}
	// The migrate script seeds the same shape ([{"m":...,"w":...}]); verify it parses identically.
	const seed = `[{"m":"crb1a","w":3},{"m":"crb1b","w":2.5},{"m":"crb1a","w":1}]`
	var fromSeed []pplnsEntry
	if err := json.Unmarshal([]byte(seed), &fromSeed); err != nil {
		t.Fatalf("migrate seed shape must parse: %v", err)
	}
	if len(fromSeed) != 3 || fromSeed[1].W != 2.5 {
		t.Fatalf("migrate seed shape mismatch: %+v", fromSeed)
	}
}
