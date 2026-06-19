package main

import (
	"path/filepath"
	"testing"
)

// A restart must hand each miner back the SAME extranonce: saveExtranonce -> loadExtranonce must
// round-trip the assignment map and never lower the counter. This is what makes a pool restart /
// HA failover transparent — in-flight shares are accepted immediately instead of being rejected
// until the next block forces the stratum bridge to push a fresh job.
func TestExtranonceSnapshotRoundTrip(t *testing.T) {
	dbMode = false
	statePath = filepath.Join(t.TempDir(), "pool.json")

	enMu.Lock()
	enAssigned = map[string]uint64{"crb1a": 1, "crb1b": 2, "crb1c": 7}
	enCounter = 7
	enMu.Unlock()
	saveExtranonce()

	// simulate a restart: wipe the in-memory map, then reload from the snapshot
	enMu.Lock()
	enAssigned = map[string]uint64{}
	enCounter = 0
	enMu.Unlock()
	loadExtranonce()

	enMu.Lock()
	a, b, c, ctr := enAssigned["crb1a"], enAssigned["crb1b"], enAssigned["crb1c"], enCounter
	enMu.Unlock()
	if a != 1 || b != 2 || c != 7 {
		t.Fatalf("assignments not restored: a=%d b=%d c=%d", a, b, c)
	}
	if ctr != 7 {
		t.Fatalf("counter not restored: got %d want 7", ctr)
	}
	// an existing miner keeps its tag; a brand-new miner gets a fresh counter beyond the restored max
	if got := extranonceFor("crb1a"); got != 1 {
		t.Fatalf("existing miner reassigned after restore: got %d want 1", got)
	}
	if got := extranonceFor("crb1d"); got != 8 {
		t.Fatalf("new miner should get counter 8 (past the restored max), got %d", got)
	}
}
