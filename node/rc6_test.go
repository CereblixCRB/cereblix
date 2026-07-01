package node

// rc6_test.go — regression guards for the 2026-07-01 RC6 fleet-wide sync-wedge fixes.
//
//   FIX1: /p2p/subscribe serves the tip from the lock-free tipSnap (not myTip()'s chain
//         RLock), so a catch-up holding the chain write lock can't pile subscribe handlers
//         into CLOSE_WAIT on advertised nodes.  -> TestSubscribeLockFreeUnderWriteLock
//   FIX2: SyncLoop re-publishes the tip snapshot every tick, so a missed OnNewBlock
//         callback can't strand /p2p/tip + /p2p/subscribe on a stale tip.
//                                                 -> TestTipSnapSelfHealsAfterMissedCallback
//   FIX3: the watchdog (only) clears the doomed-tip memo; the sync path must NOT.
//                                                 -> TestClearDoomedIsUnitOnly
//
// All three are NON-consensus: fork-choice (core.TryAdoptChain) is untouched.

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestSubscribeLockFreeUnderWriteLock proves FIX1: while the chain write lock c.mu is
// held (as during a chunked catch-up adopt), /p2p/subscribe returns promptly (lock-free
// tipSnap.Load) whereas a control reader that DOES take the RLock (/p2p/hash) blocks.
// Under the pre-RC6 code the subscribe tail called myTip() and would block for the whole
// hold — the mechanism that piled sockets into CLOSE_WAIT.
func TestSubscribeLockFreeUnderWriteLock(t *testing.T) {
	simEnv(t)
	gen := newGenChain(t)
	base := extend(t, gen, simAddr("a"), 5)
	sn := newNode(t, "n", base, base)

	const hold = 2500 * time.Millisecond
	go sn.chain.HoldWriteLockForTest(hold)
	time.Sleep(100 * time.Millisecond) // ensure the write lock is actually held

	// Control: /p2p/hash -> Chain.BlockAt -> c.mu.RLock, so it MUST block for the hold.
	// If it returns, the write lock isn't really blocking readers and the test is moot.
	ctrl := &http.Client{Timeout: 600 * time.Millisecond}
	if resp, err := ctrl.Get(sn.url + "/p2p/hash?h=1"); err == nil {
		resp.Body.Close()
		t.Fatal("control /p2p/hash returned while c.mu was held — write lock is not blocking readers; test is not discriminating")
	}

	// Wake the long-poll select repeatedly so the handler reaches its writeJSON tail.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				sn.n.fireNewBlock()
				time.Sleep(25 * time.Millisecond)
			}
		}
	}()
	defer close(stop)

	sub := &http.Client{Timeout: 800 * time.Millisecond}
	start := time.Now()
	resp, err := sub.Get(sn.url + "/p2p/subscribe")
	if err != nil {
		t.Fatalf("RC6 regression: /p2p/subscribe blocked on the chain lock while c.mu was held: %v", err)
	}
	el := time.Since(start)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/p2p/subscribe status = %d, want 200", resp.StatusCode)
	}
	if el > 700*time.Millisecond {
		t.Fatalf("/p2p/subscribe took %v while c.mu held — expected lock-free (<< the %v hold)", el, hold)
	}
}

// TestTipSnapSelfHealsAfterMissedCallback proves FIX2: even if an OnNewBlock callback is
// missed (e.g. a swallowed commit-path panic left tipSnap stale while the chain advanced),
// the periodic updateTipSnap re-publishes the true tip.
func TestTipSnapSelfHealsAfterMissedCallback(t *testing.T) {
	simEnv(t)
	gen := newGenChain(t)
	base := extend(t, gen, simAddr("b"), 7)
	sn := newNode(t, "n", base, base)

	// Simulate a missed callback: force a stale advertised tip.
	stale := tipInfo{Height: 1, Hash: "stalehash", CumWork: "01"}
	sn.n.tipSnap.Store(&stale)
	if got := sn.n.tipSnap.Load(); got == nil || got.Height != 1 {
		t.Fatalf("precondition: stale snapshot not set (got %+v)", got)
	}

	// The SyncLoop-tick republish (called directly here).
	sn.n.updateTipSnap()

	got := sn.n.tipSnap.Load()
	if got == nil {
		t.Fatal("tipSnap nil after updateTipSnap")
	}
	if got.Height != sn.chain.Height() || got.Hash != sn.chain.Tip().Hash() {
		t.Fatalf("tipSnap did not self-heal: snap h=%d hash=%s, chain h=%d hash=%s",
			got.Height, got.Hash, sn.chain.Height(), sn.chain.Tip().Hash())
	}
	if want := sn.chain.CumWork().Text(16); got.CumWork != want {
		t.Fatalf("tipSnap cumwork = %s, want %s", got.CumWork, want)
	}
}

// TestClearDoomedIsUnitOnly proves FIX3: clearDoomed forgets the doomed memo (the
// watchdog-only recovery action), while the normal isDoomed/noteDoomed contract is intact.
// The complementary invariant — that the NORMAL sync path does NOT clear doomed — is held
// by sim_test.go scenario 4 (doomed persistence across syncWithPeer rounds), which this
// change leaves untouched.
func TestClearDoomedIsUnitOnly(t *testing.T) {
	simEnv(t)
	gen := newGenChain(t)
	base := extend(t, gen, simAddr("c"), 3)
	sn := newNode(t, "n", base, base)

	const peer, tip = "http://127.0.0.1:1/", "doomedtip"
	sn.n.noteDoomed(peer, tip)
	if !sn.n.isDoomed(peer, tip) {
		t.Fatal("precondition: noteDoomed did not memoize the tip")
	}
	sn.n.clearDoomed()
	if sn.n.isDoomed(peer, tip) {
		t.Fatal("clearDoomed did not forget the doomed memo")
	}
}
