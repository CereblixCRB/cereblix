package core

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCommitStallNoLongerFreezesNode proves the fix: bbolt commit (fsync) now
// runs OUTSIDE the chain write lock c.mu (AddBlock/TryAdoptChain release c.mu
// before calling commitExtend/commitRebuild), so a slow fsync no longer
// freezes readers, sync workers, or miners. The stall hook fires during
// commitExtend — but c.mu is already released by then, so c.mu.RLock returns
// immediately rather than blocking for the stall duration.
//
// The old broken behaviour (stall UNDER c.mu) is documented in the block
// comment below for reference; this test proves it no longer applies.
func TestCommitStallNoLongerFreezesNode(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addr := "crb1" + strings.Repeat("a", 40)
	for i := 0; i < 30; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Now()
	_ = c.CumWork()
	t.Logf("baseline c.mu read (no stall): %v", time.Since(t0))

	const stallDur = 6 * time.Second
	stalling := make(chan struct{})
	var once sync.Once
	SetCommitStallForTest(func() {
		once.Do(func() { close(stalling); time.Sleep(stallDur) })
	})
	defer SetCommitStallForTest(nil)

	go func() {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			return
		}
		c.verifiedPow[b.Hash()] = true
		_ = c.AddBlock(b) // -> commitExtend -> stall hook (c.mu already RELEASED by then)
	}()

	<-stalling // the committing goroutine is now stalled OUTSIDE c.mu
	t1 := time.Now()
	_ = c.CumWork() // c.mu.RLock — must NOT block (c.mu is free)
	blocked := time.Since(t1)
	t.Logf("c.mu read DURING the stalled commit: %v (injected stall = %v)", blocked, stallDur)

	if blocked >= stallDur/2 {
		t.Fatalf("REGRESSION: read blocked for %v — c.mu is still held during the disk commit (stall = %v). The fix is broken.", blocked, stallDur)
	}
	t.Logf("CONFIRMED FIX: a stalled commit no longer freezes chain reads (blocked %v << stall %v).", blocked, stallDur)
	t.Logf("=> All sync workers, RPC and mining getwork are unaffected by disk IO stalls.")
}

// TestCommitOrderingUnderConcurrency proves the OTHER half of the fix: diskMu is
// the OUTER lock (acquired before c.mu), so the on-disk write order can never diverge
// from the in-memory commit order even when sync workers / block-push race. While one
// commit is stalled inside commitExtend (holding diskMu, c.mu released), a SECOND
// writer must block on diskMu BEFORE it can acquire c.mu — so it cannot advance the
// tip ahead of the stalled writer. Meanwhile readers stay free (proven again here).
//
// Were diskMu acquired AFTER releasing c.mu (the naive form), the second writer could
// slip in, mutate memory and reach the disk first → disk tip != memory tip → on a
// crash the node reloads the wrong fork. This test guards against that regression.
func TestCommitOrderingUnderConcurrency(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addr := "crb1" + strings.Repeat("a", 40)
	mine := func() *Block {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[b.Hash()] = true
		return b
	}
	for i := 0; i < 30; i++ {
		if err := c.AddBlock(mine()); err != nil {
			t.Fatal(err)
		}
	}
	heightAtStall := c.Height() // tip after the first (stalled) writer appends

	const stallDur = 4 * time.Second
	stalling := make(chan struct{})
	var once sync.Once
	SetCommitStallForTest(func() {
		once.Do(func() { close(stalling); time.Sleep(stallDur) })
	})
	defer SetCommitStallForTest(nil)

	// Writer A: appends one block to memory, then stalls inside commitExtend holding
	// diskMu (c.mu released). heightAtStall is the tip once A has appended.
	blockA := mine()
	heightAtStall = blockA.Height
	go func() { _ = c.AddBlock(blockA) }()
	<-stalling

	// Writer B: a second concurrent commit. It must block on diskMu (held by the
	// stalled A) BEFORE touching c.mu, so it cannot advance the tip during the stall.
	var bDone int32
	go func() {
		blockB := mine() // built on A's in-memory tip
		_ = c.AddBlock(blockB)
		atomic.StoreInt32(&bDone, 1)
	}()

	// During the stall: reader is free, and B has NOT advanced the tip.
	time.Sleep(stallDur / 2)
	rt := time.Now()
	h := c.Height()
	if blocked := time.Since(rt); blocked >= stallDur/4 {
		t.Fatalf("REGRESSION: reader blocked %v during a stalled commit", blocked)
	}
	if h != heightAtStall {
		t.Fatalf("REGRESSION: tip advanced to %d during the stall (want %d) — a second writer slipped past diskMu ordering", h, heightAtStall)
	}
	if atomic.LoadInt32(&bDone) != 0 {
		t.Fatalf("REGRESSION: second writer committed during the stall — diskMu did not serialize it")
	}
	t.Logf("CONFIRMED: during a %v stalled commit the reader was free and the 2nd writer stayed blocked on diskMu (tip pinned at %d).", stallDur, heightAtStall)

	// After the stall, B proceeds and the tip advances by exactly one.
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt32(&bDone) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := c.Height(); got != heightAtStall+1 {
		t.Fatalf("after stall: tip = %d, want %d", got, heightAtStall+1)
	}
	t.Logf("CONFIRMED: after the stall the 2nd writer committed in order (tip %d).", c.Height())
}

// brokeNote is the old behavior for reference — do NOT reinstate:
// bbolt commit (fsync) ran UNDER c.mu.Lock() in AddBlock/TryAdoptChain.
// A slow fsync held c.mu for minutes → all c.mu.RLock callers (sync workers,
// RPC, mining getwork) blocked → total log silence → node appeared dead →
// only a restart recovered. Confirmed via goroutine dump: goroutine 207 in
// TryAdoptChain → commitRebuild → bbolt.DB.Update [runnable, 18 min],
// 15+ goroutines in sync.RWMutex.RLock [18 minutes].
const brokeNote = "pre-fix: commitExtend/Rebuild called c.mu.Lock-held -> freeze"
