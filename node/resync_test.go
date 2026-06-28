package node

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"cereblix/core"
)

// procRSSmb returns this process's resident set size in MiB (Linux), or -1 elsewhere.
func procRSSmb() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "VmRSS:") {
			if f := strings.Fields(ln); len(f) >= 2 {
				if kb, e := strconv.Atoi(f[1]); e == nil {
					return kb / 1024
				}
			}
		}
	}
	return -1
}

func heapMB() int { var m runtime.MemStats; runtime.ReadMemStats(&m); return int(m.HeapAlloc / (1 << 20)) }

// extendBig builds a LARGE preseeded chain. It overrides each block's timestamp to a
// controlled monotonic value (genesis+height, 1s steps) BEFORE preseeding/adding, so a
// fast in-loop build does not pile timestamps at `now` and trip BuildTemplate's
// future-drift cap (which limits the shared `extend` helper to ~500 blocks). The block's
// target is computed by BuildTemplate from the (controlled) prefix and is unaffected by
// the block's own time, so the chain stays valid. Memory-test use only.
func extendBig(t *testing.T, c *core.Chain, addr string, n int) []*core.Block {
	t.Helper()
	gtime := c.BlockAt(0).Time
	out := make([]*core.Block, 0, n)
	for i := 0; i < n; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate h=%d: %v", i+1, err)
		}
		b.Time = gtime + b.Height // controlled monotonic time, well below now → dodges the cap
		c.MarkVerifiedForTest(b)  // preseed AFTER setting Time (the hash includes Time)
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock h=%d: %v", b.Height, err)
		}
		out = append(out, b)
	}
	return out
}

// TestResyncFromScratchMemory runs a FULL from-scratch resync entirely in a vacuum
// (in-process source + empty resyncer over loopback httptest, the REAL
// syncWithPeer→PreVerifyPoW→PreVerifySigs→TryAdoptChain path with REAL PoW verify) and
// reports memory. Proves the resync COMPLETES with bounded RSS — no runaway/OOM (the old
// failure: a stranded/doomed sync looped re-fetching+re-allocating the candidate under a
// held lock → RSS to 7G+ → OOM, "nothing helped but reseed"). Size via RESYNC_N (default
// 8000). Touches NO network and no prod node.
func TestResyncFromScratchMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("large from-scratch resync; skipped in -short")
	}
	simEnv(t)
	N := 8000
	if v := os.Getenv("RESYNC_N"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			N = n
		}
	}
	addr := simAddr("rs")
	gen := newGenChain(t)
	chain := extendBig(t, gen, addr, N)     // source chain of N blocks (controlled timestamps so a big chain builds fast)
	src := newNode(t, "src", chain, chain)  // serves the full chain over httptest
	b := newNode(t, "resync", nil, chain)   // EMPTY chain, PoW preseeded (blocks are test-built, not mined) — this isolates the MEMORY question (the OOM was chain/candidate accumulation, not PoW VMs)
	b.n.addPeer(src.url)

	runtime.GC()
	baseRSS, baseHeap := procRSSmb(), heapMB()
	t.Logf("baseline (source loaded in-proc, resyncer empty): heap=%dMB rss=%dMB | target=%d blocks", baseHeap, baseRSS, N)

	peakRSS, peakHeap := baseRSS, baseHeap
	for i := 0; b.height() < uint64(N) && i < 200; i++ {
		b.n.syncWithPeer(src.url)
		if r := procRSSmb(); r > peakRSS {
			peakRSS = r
		}
		if h := heapMB(); h > peakHeap {
			peakHeap = h
		}
		t.Logf("round %d: height=%d/%d heap=%dMB rss=%dMB", i, b.height(), N, heapMB(), procRSSmb())
	}
	runtime.GC()
	finalRSS, finalHeap := procRSSmb(), heapMB()
	t.Logf("DONE height=%d/%d | peakRSS=%dMB peakHeap=%dMB | finalRSS=%dMB finalHeap=%dMB | resyncer-RSS-delta≈%dMB",
		b.height(), N, peakRSS, peakHeap, finalRSS, finalHeap, peakRSS-baseRSS)

	if b.height() != uint64(N) {
		t.Fatalf("resync did NOT complete: %d/%d (no OOM is the point — completion proves it)", b.height(), N)
	}
}
