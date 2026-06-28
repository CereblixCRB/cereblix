package node

// sim_test.go — an in-vacuum, in-process multi-node testnet that reproduces the
// v2.3.1 fork-deadlock / FD-death incident and proves the v2.4.0 fixes resolve it.
//
// Everything runs on loopback via httptest.NewServer over the REAL node.P2PHandler
// and the REAL core.Chain fork-choice / sync code (syncWithPeer → PreVerifyPoW →
// TryAdoptChain). No network egress: the baked-in public fallbackSeeds are blanked
// for the duration of the test and the SSRF guard is opened ONLY for 127.0.0.0/8.
//
// PoW is preseeded (core.MarkVerifiedForTest) exactly like the in-package core tests
// do via c.verifiedPow[...] — mining real memory-hard NeuroMorph PoW would make a
// multi-hundred-block sim take many minutes. Fork-choice, tie-break, reorg depth,
// deep-recovery gating and peer-health are all exercised for real.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cereblix/core"
)

// ---------------------------------------------------------------- test scaffolding

// simEnv blanks the public fallback seeds (no network) and opens the SSRF guard for
// loopback so nodes can peer over httptest 127.0.0.1 URLs. Restored on cleanup.
func simEnv(t *testing.T) {
	t.Helper()
	savedSeeds := fallbackSeeds
	fallbackSeeds = nil
	SetTrustedSubnets("127.0.0.0/8")
	t.Cleanup(func() {
		fallbackSeeds = savedSeeds
		SetTrustedSubnets("")
	})
}

func simAddr(tag string) string {
	h := sha256.Sum256([]byte("sim/" + tag))
	return "crb1" + hex.EncodeToString(h[:])[:40]
}

func newGenChain(t *testing.T) *core.Chain {
	t.Helper()
	var c *core.Chain
	var err error
	if os.Getenv("SIM_BBOLT") == "1" {
		c, err = core.OpenChain(t.TempDir(), true, false) // bbolt store (the fleet's -store bbolt)
	} else {
		c, err = core.NewChain(t.TempDir()) // jsonl (test default)
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// extend appends n PoW-preseeded blocks (paying to addr) to c and returns them.
func extend(t *testing.T, c *core.Chain, addr string, n int) []*core.Block {
	t.Helper()
	out := make([]*core.Block, 0, n)
	for i := 0; i < n; i++ {
		b, err := c.BuildTemplate(addr)
		if err != nil {
			t.Fatalf("BuildTemplate: %v", err)
		}
		c.MarkVerifiedForTest(b)
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock h=%d: %v", b.Height, err)
		}
		out = append(out, b)
	}
	return out
}

// loadBase loads a base prefix (heights 1..) into c via the depth-0 fast path.
func loadBase(t *testing.T, c *core.Chain, base []*core.Block) {
	t.Helper()
	c.MarkVerifiedForTest(base...)
	if err := c.TryAdoptChain(base[0].Height, base); err != nil {
		t.Fatalf("loadBase: %v", err)
	}
}

// countHandler wraps a P2P handler and counts requests per URL path so a test can
// assert (e.g.) that a doomed peer stops being re-fetched.
type countHandler struct {
	h     http.Handler
	mu    sync.Mutex
	count map[string]int64
}

func (c *countHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.count[r.URL.Path]++
	c.mu.Unlock()
	c.h.ServeHTTP(w, r)
}

func (c *countHandler) get(p string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count[p]
}

type simNode struct {
	name   string
	n      *Node
	chain  *core.Chain
	srv    *httptest.Server
	url    string
	counts *countHandler
}

// newNode builds a node whose chain trusts every block in `known` (so live sync
// needn't re-mine), optionally pre-loaded with `initial`, served over httptest.
func newNode(t *testing.T, name string, initial, known []*core.Block) *simNode {
	t.Helper()
	c := newGenChain(t)
	if len(known) > 0 {
		c.MarkVerifiedForTest(known...)
	}
	if len(initial) > 0 {
		if err := c.TryAdoptChain(initial[0].Height, initial); err != nil {
			t.Fatalf("%s: load initial: %v", name, err)
		}
	}
	n := New(c, t.TempDir(), "", nil)
	ch := &countHandler{h: n.P2PHandler(), count: map[string]int64{}}
	srv := httptest.NewServer(ch)
	sn := &simNode{name: name, n: n, chain: c, srv: srv, url: srv.URL, counts: ch}
	t.Cleanup(srv.Close)
	return sn
}

func (s *simNode) tip() string  { return s.chain.Tip().Hash() }
func (s *simNode) height() uint64 { return s.chain.Height() }

// gossip = one full mesh round: every node pulls from every other (mirrors a
// SyncLoop tick but synchronous + deterministic).
func gossip(nodes []*simNode) {
	for _, a := range nodes {
		for _, b := range nodes {
			if a == b {
				continue
			}
			a.n.syncWithPeer(b.url)
		}
	}
}

func converged(nodes []*simNode) bool {
	first := nodes[0].tip()
	for _, n := range nodes[1:] {
		if n.tip() != first {
			return false
		}
	}
	return true
}

func tipList(nodes []*simNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.tip()[:12]
	}
	return out
}

// safeBuf is a concurrency-safe log sink (broadcast goroutines may log concurrently).
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func timeGet(cl *http.Client, url string) (time.Duration, bool) {
	start := time.Now()
	resp, err := cl.Get(url)
	d := time.Since(start)
	if err != nil {
		return d, false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return time.Since(start), resp.StatusCode == 200
}

// ----------------------------------------------------------------- the simulation

func TestIncidentSimV240(t *testing.T) {
	simEnv(t)

	t.Run("1_equal_work_fork_converges_RC1", scenarioEqualWorkFork)
	t.Run("2_far_behind_catchup_stays_responsive_RC2_RC4", scenarioCatchupResponsive)
	t.Run("3_deep_reorg_recovery_gated_v4", scenarioDeepRecovery)
	t.Run("4_doomed_tip_and_dead_peer_RC4", scenarioPeerHealth)
}

// Scenario 1 (RC1): N nodes start on competing SAME-HEIGHT equal-work tips (the
// 24535 fork shape). v2.3.1 would chase a peer on tip-hash alone and loop forever on
// "adopt failed". v2.4.0's tie-break-gated pursuit (smaller hash wins) must make ALL
// nodes converge to ONE identical tip and then STOP logging "adopt failed".
func scenarioEqualWorkFork(t *testing.T) {
	gen := newGenChain(t)
	const baseH = 6
	base := extend(t, gen, simAddr("base1"), baseH) // shared heights 1..6

	// Four competing height-7 blocks (distinct coinbase → distinct hash, identical
	// target → identical cumulative work). Built on `gen` WITHOUT advancing it.
	var tips []*core.Block
	for i := 0; i < 4; i++ {
		b, err := gen.BuildTemplate(simAddr(fmt.Sprintf("fork1-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		gen.MarkVerifiedForTest(b)
		tips = append(tips, b)
	}
	known := append(append([]*core.Block{}, base...), tips...)

	want := tips[0].Hash()
	for _, b := range tips {
		if b.Hash() < want {
			want = b.Hash()
		}
	}

	nodes := make([]*simNode, len(tips))
	for i := range tips {
		nodes[i] = newNode(t, fmt.Sprintf("n%d", i), append(append([]*core.Block{}, base...), tips[i]), known)
		if nodes[i].height() != baseH+1 {
			t.Fatalf("%s: bad start height %d", nodes[i].name, nodes[i].height())
		}
	}
	for _, a := range nodes {
		for _, b := range nodes {
			if a != b {
				a.n.addPeer(b.url)
			}
		}
	}

	t.Logf("start tips (all height %d, equal work): %v", baseH+1, tipList(nodes))
	if converged(nodes) {
		t.Fatal("precondition: nodes should START diverged")
	}

	const maxRounds = 12
	rounds := 0
	for ; rounds < maxRounds && !converged(nodes); rounds++ {
		gossip(nodes)
	}
	if !converged(nodes) {
		t.Fatalf("RC1 NOT FIXED: %d nodes did not converge after %d rounds: tips=%v", len(nodes), maxRounds, tipList(nodes))
	}
	got := nodes[0].tip()
	if got != want {
		t.Fatalf("converged to %s but the deterministic tie-break winner is %s (smallest hash)", got[:12], want[:12])
	}
	t.Logf("PASS: converged in %d round(s) to the smallest-hash tip %s (all %d nodes agree)", rounds, want[:12], len(nodes))

	// RC1 core claim: at steady state the node STOPS logging "adopt failed".
	buf := &safeBuf{}
	log.SetOutput(buf)
	gossip(nodes) // one extra round once converged
	gossip(nodes)
	log.SetOutput(os.Stderr)
	if n := strings.Count(buf.String(), "adopt failed"); n != 0 {
		t.Fatalf("RC1: still logging 'adopt failed' %d time(s) at steady state:\n%s", n, buf.String())
	}
	t.Logf("PASS: zero 'adopt failed' log lines across 2 post-convergence rounds")
}

// Scenario 2 (RC2/RC4): a node 500+ blocks behind catches up via the REAL sync path
// (PreVerifyPoW off-lock, then a bounded, chunked TryAdoptChain). While it catches up
// it must keep SERVING its P2P endpoints promptly — the incident wedged here because
// TryAdoptChain held the chain WRITE lock across PoW verification, starving read
// handlers until the FD ceiling ("accept4: too many open files"). We assert the
// lock-free /p2p/tip snapshot AND the RLock /p2p/hash + /p2p/blocks endpoints all stay
// responsive throughout, and the node reaches the network tip.
func scenarioCatchupResponsive(t *testing.T) {
	const N = 520 // > batchBlocks (200) and > adoptChunk (256): forces chunked catch-up
	gen := newGenChain(t)
	canon := extend(t, gen, simAddr("canon2"), N)

	provider := newNode(t, "provider", canon, canon)
	behind := newNode(t, "behind", nil, canon) // starts at genesis (height 0)
	behind.n.addPeer(provider.url)
	if behind.height() != 0 || provider.height() != N {
		t.Fatalf("bad start: behind=%d provider=%d", behind.height(), provider.height())
	}

	cl := &http.Client{Timeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		behind.n.syncWithPeer(provider.url) // the heavy catch-up
		close(done)
	}()

	var maxTip, maxHash, maxBlocks time.Duration
	var samples, tipErr, otherErr int
poll:
	for {
		select {
		case <-done:
			break poll
		default:
		}
		if d, ok := timeGet(cl, behind.url+"/p2p/tip"); ok { // lock-free snapshot path
			if d > maxTip {
				maxTip = d
			}
		} else {
			tipErr++
		}
		if d, ok := timeGet(cl, behind.url+"/p2p/hash?h=0"); ok { // RLock path
			if d > maxHash {
				maxHash = d
			}
		} else {
			otherErr++
		}
		if d, ok := timeGet(cl, behind.url+"/p2p/blocks?from=1&count=8"); ok { // RLock path
			if d > maxBlocks {
				maxBlocks = d
			}
		} else {
			otherErr++
		}
		samples++
	}
	<-done

	if behind.height() != N {
		t.Fatalf("RC4: behind did not catch up: height=%d want=%d", behind.height(), N)
	}
	if behind.tip() != provider.tip() {
		t.Fatalf("behind tip %s != provider tip %s", behind.tip()[:12], provider.tip()[:12])
	}
	t.Logf("caught up %d blocks; %d poll samples while catching up", N, samples)
	t.Logf("max latency DURING catch-up: /p2p/tip=%v (lock-free)  /p2p/hash=%v  /p2p/blocks=%v", maxTip, maxHash, maxBlocks)
	if tipErr != 0 {
		t.Fatalf("RC2: /p2p/tip errored %d time(s) during catch-up (node was wedged)", tipErr)
	}
	if samples < 5 {
		t.Fatalf("too few samples (%d) — catch-up finished suspiciously fast; can't claim responsiveness", samples)
	}
	// Generous absolute ceiling: a wedged node would block for whole-catch-up seconds.
	const ceiling = 750 * time.Millisecond
	if maxTip > ceiling || maxHash > ceiling || maxBlocks > ceiling {
		t.Fatalf("RC2: an endpoint stalled past %v during catch-up (tip=%v hash=%v blocks=%v)", ceiling, maxTip, maxHash, maxBlocks)
	}
	t.Logf("PASS: node served P2P endpoints throughout a %d-block catch-up (no wedge); other-endpoint errors=%d", N, otherErr)
}

// Scenario 3 (Stage 2 hardfork): a node stranded on a losing fork that is DEEPER than
// MaxReorgDepth re-converges to the authority chain ONLY when (a) consensus v4 is
// active AND (b) a matching SIGNED authority anchor is present. With no/wrong anchor,
// or with v4 inactive, it must stay stranded (byte-identical to the old hard reject).
func scenarioDeepRecovery(t *testing.T) {
	run := func(t *testing.T, baseN int, wantActive bool) (stranded, authority *simNode, authTop *core.Block, ours []*core.Block) {
		gen := newGenChain(t)
		base := extend(t, gen, simAddr("base3"), baseN)

		ga := newGenChain(t)
		loadBase(t, ga, base)
		auth := extend(t, ga, simAddr("auth3"), 4) // heavier authority branch (+4)

		go2 := newGenChain(t)
		loadBase(t, go2, base)
		ours = extend(t, go2, simAddr("ours3"), 2) // our losing branch (+2)

		known := append(append(append([]*core.Block{}, base...), auth...), ours...)
		authority = newNode(t, "authority", append(append([]*core.Block{}, base...), auth...), known)
		stranded = newNode(t, "stranded", append(append([]*core.Block{}, base...), ours...), known)
		stranded.chain.MaxReorgDepth = 1 // make the +2 fork a "too deep" reorg
		stranded.n.addPeer(authority.url)
		authTop = auth[len(auth)-1]
		return
	}

	// --- v4 ACTIVE (base 102 → chain length > SignalWindow, all v4-signaled) ---
	t.Run("active_no_anchor_stays_stranded", func(t *testing.T) {
		stranded, authority, authTop, ours := run(t, 102, true)
		oursTop := ours[len(ours)-1].Hash()
		for i := 0; i < 4; i++ {
			stranded.n.syncWithPeer(authority.url)
		}
		if stranded.tip() != oursTop {
			t.Fatalf("no-anchor: should stay stranded on our fork, but tip=%s", stranded.tip()[:12])
		}
		if stranded.tip() == authTop.Hash() {
			t.Fatal("no-anchor: must NOT have adopted the authority chain")
		}
		t.Logf("PASS: v4 active, NO anchor → stayed stranded (deep reorg rejected)")
	})

	t.Run("active_wrong_anchor_stays_stranded", func(t *testing.T) {
		stranded, authority, authTop, ours := run(t, 102, true)
		oursTop := ours[len(ours)-1].Hash()
		stranded.chain.SetAuthorityAnchor(core.Checkpoint{Height: authTop.Height, Hash: strings.Repeat("0", 64)})
		for i := 0; i < 4; i++ {
			stranded.n.syncWithPeer(authority.url)
		}
		if stranded.tip() != oursTop {
			t.Fatalf("wrong-anchor: should stay stranded, tip=%s", stranded.tip()[:12])
		}
		t.Logf("PASS: v4 active, WRONG anchor → stayed stranded")
	})

	t.Run("active_matching_anchor_recovers", func(t *testing.T) {
		stranded, authority, authTop, _ := run(t, 102, true)
		stranded.chain.SetAuthorityAnchor(core.Checkpoint{Height: authTop.Height, Hash: authTop.Hash()})
		converr := false
		for i := 0; i < 6 && stranded.tip() != authTop.Hash(); i++ {
			stranded.n.syncWithPeer(authority.url)
		}
		if stranded.tip() != authTop.Hash() {
			converr = true
		}
		if converr {
			t.Fatalf("matching-anchor: deep recovery FAILED, tip=%s want=%s", stranded.tip()[:12], authTop.Hash()[:12])
		}
		if stranded.height() != authority.height() {
			t.Fatalf("matching-anchor: height %d != authority %d", stranded.height(), authority.height())
		}
		t.Logf("PASS: v4 active, MATCHING signed anchor → recovered to authority tip %s (h=%d)", authTop.Hash()[:12], stranded.height())
	})

	// --- v4 INACTIVE (short chain < SignalWindow): must behave like old v3 ---
	t.Run("inactive_matching_anchor_stays_stranded", func(t *testing.T) {
		stranded, authority, authTop, ours := run(t, 10, false)
		oursTop := ours[len(ours)-1].Hash()
		stranded.chain.SetAuthorityAnchor(core.Checkpoint{Height: authTop.Height, Hash: authTop.Hash()})
		for i := 0; i < 4; i++ {
			stranded.n.syncWithPeer(authority.url)
		}
		if stranded.tip() != oursTop {
			t.Fatalf("inactive: gate must stay closed, but tip=%s", stranded.tip()[:12])
		}
		t.Logf("PASS: v4 INACTIVE + matching anchor → STILL stranded (consensus-identical to v3)")
	})
}

// Scenario 4 (RC4): (a) a peer permanently on a losing fork is fetched ONCE, then
// memoized as doomed and skipped — not re-fetched every round; (b) a dead/blackhole
// peer fails fast and does NOT stall the round, and a good peer still syncs.
func scenarioPeerHealth(t *testing.T) {
	t.Run("doomed_tip_not_refetched", func(t *testing.T) {
		gen := newGenChain(t)
		base := extend(t, gen, simAddr("base4"), 5)

		go2 := newGenChain(t)
		loadBase(t, go2, base)
		ours := extend(t, go2, simAddr("ours4"), 2) // our +2
		oursTop := ours[len(ours)-1].Hash()

		// Peer: a competing EQUAL-work +2 fork (depth 2, no depth-1 tie-break) whose
		// tip hash is SMALLER than ours, so syncWithPeer pursues it — and core then
		// rejects it as "lacks sufficient work" → it gets memoized as doomed.
		var peerBlocks []*core.Block
		for i := 0; ; i++ {
			gp := newGenChain(t)
			loadBase(t, gp, base)
			pb := extend(t, gp, simAddr(fmt.Sprintf("peer4-%d", i)), 2)
			if pb[len(pb)-1].Hash() < oursTop {
				peerBlocks = pb
				break
			}
			if i > 400 {
				t.Fatal("could not synthesize a smaller-hash competing fork")
			}
		}
		peerTip := peerBlocks[len(peerBlocks)-1].Hash()
		known := append(append(append([]*core.Block{}, base...), ours...), peerBlocks...)

		peer := newNode(t, "loser", append(append([]*core.Block{}, base...), peerBlocks...), known)
		stranded := newNode(t, "us", append(append([]*core.Block{}, base...), ours...), known)
		stranded.n.addPeer(peer.url)

		stranded.n.syncWithPeer(peer.url) // round 1: should fetch + reject + memo doomed
		after1 := peer.counts.get("/p2p/blocks")
		if after1 == 0 {
			t.Fatal("precondition: round 1 should have fetched blocks from the losing peer")
		}
		if !stranded.n.isDoomed(peer.url, peerTip) {
			t.Fatal("RC4: losing peer's tip was NOT memoized as doomed")
		}
		for i := 0; i < 6; i++ {
			stranded.n.syncWithPeer(peer.url) // rounds 2..7: must SKIP the doomed tip
		}
		after7 := peer.counts.get("/p2p/blocks")
		if stranded.tip() != oursTop {
			t.Fatalf("we must stay on our (winning) fork, tip=%s", stranded.tip()[:12])
		}
		if after7 != after1 {
			t.Fatalf("RC4 NOT FIXED: doomed peer re-fetched: /p2p/blocks %d → %d across 6 extra rounds", after1, after7)
		}
		t.Logf("PASS: doomed peer fetched %d time(s) in round 1, then 0 re-fetches across 6 rounds (count steady at %d)", after1, after7)
	})

	t.Run("dead_peer_does_not_stall_round", func(t *testing.T) {
		gen := newGenChain(t)
		canon := extend(t, gen, simAddr("canon4b"), 8)

		good := newNode(t, "good", canon, canon)
		behind := newNode(t, "behind4b", nil, canon)

		// A blackhole peer that fails fast (HTTP 500 on everything).
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "blackhole", 500)
		}))
		t.Cleanup(dead.Close)

		behind.n.addPeer(good.url)
		behind.n.addPeer(dead.URL)

		// Bounded-concurrency fan-out (exactly what SyncLoop does): a dead peer must
		// not block the good one.
		start := time.Now()
		fanoutRound(behind.n, []string{good.url, dead.URL})
		elapsed := time.Since(start)

		if behind.height() != 8 {
			t.Fatalf("RC4: behind failed to sync the good chain (height=%d) — dead peer stalled the round", behind.height())
		}
		if behind.n.peerReady(dead.URL) {
			t.Fatal("RC4: dead peer should be in backoff after a failed attempt")
		}
		if elapsed > 3*time.Second {
			t.Fatalf("RC4: round took %v — a dead peer stalled it", elapsed)
		}
		t.Logf("PASS: dead peer (HTTP 500) fast-failed + backed off; good peer synced 8 blocks; round took %v", elapsed.Round(time.Millisecond))

		// Stronger: a HANGING peer must not delay a good peer either (bounded fan-out).
		release := make(chan struct{})
		hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release // block until released
		}))
		behind2 := newNode(t, "behind4c", nil, canon)
		behind2.n.addPeer(good.url)
		behind2.n.addPeer(hang.URL)
		hangDone := make(chan struct{})
		go func() {
			behind2.n.syncWithPeer(hang.URL) // will block on the hung server
			close(hangDone)
		}()
		gstart := time.Now()
		behind2.n.syncWithPeer(good.url) // must complete promptly regardless of the hang
		gelapsed := time.Since(gstart)
		if behind2.height() != 8 {
			t.Fatalf("RC4: good peer sync blocked behind a hung peer (height=%d)", behind2.height())
		}
		if gelapsed > 3*time.Second {
			t.Fatalf("RC4: good-peer sync took %v while another peer hung", gelapsed)
		}
		t.Logf("PASS: good-peer sync completed in %v while a separate peer was hung (no head-of-line block)", gelapsed.Round(time.Millisecond))
		close(release)
		<-hangDone
		hang.Close()
	})
}

// fanoutRound replicates SyncLoop's bounded-concurrency contact of a peer set.
func fanoutRound(n *Node, peers []string) {
	sem := make(chan struct{}, syncConcurrency)
	var wg sync.WaitGroup
	for _, p := range peers {
		if !n.peerReady(p) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			n.syncWithPeer(p)
		}(p)
	}
	wg.Wait()
}
