package core

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// TestPreVerifyPoWSeedKeyedSkip is the consensus-safety guard for the off-lock
// PoW pre-verification (PreVerifyPoW + the powOK skip in validateBlock):
//   - a powOK entry under the CORRECT epoch seed lets validateBlock skip the re-hash
//     and accept the block (this is the whole point — the hashing moved off-lock);
//   - a powOK entry under the WRONG seed must NOT let a bad-PoW block through:
//     validateBlock must miss the cache, re-hash, and reject it.
// This proves a stale/mismatched pre-verify (e.g. a reorg that changed the epoch
// boundary) can never bypass PoW, so the accept/reject outcome stays consensus-exact.
func TestPreVerifyPoWSeedKeyedSkip(t *testing.T) {
	addr := "crb1" + strings.Repeat("d", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A few real (PoW-preseeded) blocks so we have a prefix to build a candidate on.
	for i := 0; i < 3; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate %d: %v", i, err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock %d: %v", i, err)
		}
	}

	// Candidate that extends the tip but is UNMINED (its real PoW does not meet target).
	cand, err := c.BuildTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := epochSeedFor(c.blocks, cand.Height)
	correct := hex.EncodeToString(seed)
	wrong := hex.EncodeToString(append([]byte{seed[0] ^ 0xFF}, seed[1:]...))

	// 1) WRONG-seed powOK entry: must be ignored -> re-hash -> reject (bad PoW). This
	//    runs first because it fails and therefore does not mutate the chain.
	c.powOK[powKey{wrong, cand.Hash()}] = true
	if err := c.TryAdoptChain(cand.Height, []*Block{cand}); err == nil {
		t.Fatal("wrong-seed powOK must NOT bypass PoW — a bad-PoW block was accepted")
	} else if !strings.Contains(err.Error(), "proof of work") {
		t.Fatalf("expected insufficient-PoW rejection, got: %v", err)
	}
	if c.Height() != cand.Height-1 {
		t.Fatalf("chain advanced on a rejected block: height=%d", c.Height())
	}

	// 2) CORRECT-seed powOK entry: validateBlock skips the re-hash and accepts.
	c.powOK[powKey{correct, cand.Hash()}] = true
	if err := c.TryAdoptChain(cand.Height, []*Block{cand}); err != nil {
		t.Fatalf("correct-seed powOK should let the block adopt (PoW skipped): %v", err)
	}
	if c.Tip().Hash() != cand.Hash() {
		t.Fatalf("tip did not advance to the adopted candidate")
	}
}

// TestPreVerifyPoWConcurrent exercises the new lock-free paths under -race:
// PreVerifyPoW (RLock snapshot + off-lock hashing + powMu) runs concurrently with
// read handlers (Tip/Height/CumWork/BuildTemplate — now RLock) while a writer adopts
// blocks. Asserts no data race and a consistent final chain.
func TestPreVerifyPoWConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("hashing-heavy; skipped in -short")
	}
	addr := "crb1" + strings.Repeat("e", 40)
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var seedBlocks []*Block
	for i := 0; i < 6; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatal(err)
		}
		seedBlocks = append(seedBlocks, b)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Readers: the hot RLock accessors that used to starve behind the write lock.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = c.Tip()
				_ = c.Height()
				_ = c.CumWork()
				_, _ = c.BuildTemplate(addr)
			}
		}()
	}
	// Pre-verifiers: run the off-lock PoW path repeatedly (bounded; hashing is heavy).
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				c.PreVerifyPoW(1, seedBlocks)
			}
		}()
	}
	// Writer: keep adopting new tip blocks while the above run.
	for i := 0; i < 25; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock during concurrency: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	if c.Height() != 31 { // 6 + 25
		t.Fatalf("unexpected final height %d (want 31)", c.Height())
	}
}
