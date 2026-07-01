// Package node wires the Cereblix chain into a P2P + RPC daemon with an
// optional built-in CPU miner.
package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

const (
	syncInterval        = 3 * time.Second  // poll fallback; faster catch-up = fewer orphans for poll-only nodes
	subscribeHold       = 20 * time.Second // how long the server holds a /p2p/subscribe long-poll (< server WriteTimeout)
	rebroadcastInterval = 60 * time.Second // periodic re-flood of unconfirmed mempool txns (gossip backstop)
	batchBlocks         = 200
	templateMaxAge      = 8 * time.Second
	templateRefresh     = 5 * time.Second // min interval between getwork template rebuilds (cf. Bitcoin GBT's few-second cache)
	// Cap simultaneously-open accepted connections per listener. Generous for honest
	// load (~tens of peers + the local stratum bridge), far below the FD ceiling: a
	// handler stall (every HTTP handler blocked on the chain RLock while a long
	// catch-up holds the write lock) must NOT pile up sockets until the process hits
	// "accept4: too many open files" and wedges until a manual restart. See limitListener.
	maxConnsP2P = 1024
	maxConnsRPC = 1024
	// adoptChunk bounds how many blocks one TryAdoptChain applies under the chain write
	// lock during catch-up, so each hold is short and read handlers interleave between
	// chunks (paired with Chain.PreVerifyPoW, which moves the memory-hard PoW off-lock).
	// Applies to pure EXTENSIONS only; a reorg candidate is always adopted whole (it
	// must outweigh our chain as a unit, so it can't be chunked).
	adoptChunk = 256
	// syncConcurrency caps how many peers SyncLoop contacts at once. The loop used to be
	// serial, so one dead/slow peer (a full HTTP timeout) stalled the whole round and the
	// node fell behind → heavy catch-up → lock pressure. Bounded fan-out keeps a round
	// short without unbounded goroutine/connection growth.
	syncConcurrency = 6
)

// fallbackSeeds are baked-in public nodes used to bootstrap and to keep the mesh
// connected even if a configured/DNS seed is temporarily down. Additive:
// addr-gossip + discovery take over once connected, and an unreachable entry is
// simply marked dead. Mirrors Bitcoin's hardcoded seed fallback.
var fallbackSeeds = []string{
	"http://seed.cereblix.com:18750",
	"http://186.246.11.2:18750",   // relay (ru.cereblix.com)
	"http://13.140.141.180:18750", // .180 stratum node (also a seed.cereblix.com A-record)
	"http://13.140.142.95:18750",  // .95 pool-standby node (also a seed.cereblix.com A-record)
	// Removed 2026-06-28: 188.34.181.191 (head — DECOMMISSIONED 2026-06-21, dead) and
	// 13.140.141.179 (now web-origin, runs NO node). Both were re-added to every node's
	// peers.json on each boot and dialed forever. See cereblix-node-fork-deadlock.
}

// skipFallbackSeeds, set by SetIsolated (-isolated flag), makes New() NOT add the
// baked-in public seeds — so a node peers ONLY with its explicit -peers. Used for an
// in-vacuum testnet (no contact with the live network). Default off; never set in prod.
var skipFallbackSeeds bool

// SetIsolated toggles vacuum mode (skip baked-in fallback seeds). Call before node.New.
func SetIsolated(b bool) { skipFallbackSeeds = b }

// workTemplate is one mining job: an unmined block plus when it was built. Several
// jobs can coexist for a single tip (each mempool-changing refresh publishes a new
// one), so a miner that solves a slightly-older job is still accepted on submit.
type workTemplate struct {
	blk  *core.Block
	born time.Time
}

// peerHealth tracks per-peer sync reliability so one dead/wedged/doomed peer cannot
// drain the (now bounded-concurrency) sync round: failures back off exponentially and
// a peer that keeps failing is evicted; lastDoomed remembers a tip this peer served
// that core rejected, so we stop re-fetching the identical losing chain every tick.
type peerHealth struct {
	fails      int
	nextTry    time.Time
	lastDoomed string
}

type Node struct {
	Chain *core.Chain

	dataDir      string
	publicURL    string // advertised to peers, may be empty
	Version      string // node software version, surfaced in /api/status
	StallRestart bool   // -stall-restart: watchdog may exit(1) for a supervisor restart if hard-stuck (default off)

	peersMu sync.Mutex
	peers   map[string]time.Time // base URL -> last success

	phMu    sync.Mutex
	ph      map[string]*peerHealth  // per-peer sync health: backoff + last doomed tip
	tipSnap atomic.Pointer[tipInfo] // cached current tip; lets /p2p/tip answer without taking the chain lock
	seeds   []string                // configured -peers seeds; never evicted (with fallbackSeeds)

	client    *http.Client
	subClient *http.Client // longer timeout, for /p2p/subscribe long-polling

	notifyMu sync.Mutex
	notifyCh chan struct{} // closed+replaced on each adopted block (long-poll fan-out)

	subMu       sync.Mutex
	subscribing map[string]bool // peers we currently hold a subscribe loop for

	tmplMu     sync.Mutex
	templates  map[string]*workTemplate // work id -> job (current + recent same-tip, kept valid for late submits)
	tmplLatest map[string]string        // "tipHash|addr" -> current work id
	tmplSeq    uint64                   // monotonic; makes each work id unique

	cpMu       sync.Mutex
	checkpoint core.Checkpoint // latest signed authority checkpoint we hold/serve

	upgMu sync.RWMutex
	upg   *core.UpgradeManifest // latest authority-signed upgrade manifest we hold/serve

	hashCount atomic.Uint64 // built-in miner counter
	stop      chan struct{}

	concentrationHigh bool // 51%-watch hysteresis (SyncLoop-only, no lock)
}

// SetUpgrade stores the latest authority-verified upgrade manifest so the node
// can re-serve it to peers and the website (RU-friendly mirror of GitHub).
func (n *Node) SetUpgrade(m core.UpgradeManifest) {
	n.upgMu.Lock()
	n.upg = &m
	n.upgMu.Unlock()
}

func New(chain *core.Chain, dataDir, publicURL string, seedPeers []string) *Node {
	n := &Node{
		Chain:       chain,
		dataDir:     dataDir,
		publicURL:   strings.TrimRight(publicURL, "/"),
		peers:       map[string]time.Time{},
		ph:          map[string]*peerHealth{},
		seeds:       append([]string(nil), seedPeers...),
		client:      safePeerClient(),
		subClient:   &http.Client{Timeout: subscribeHold + 15*time.Second, Transport: safePeerTransport()},
		templates:   map[string]*workTemplate{},
		tmplLatest:  map[string]string{},
		notifyCh:    make(chan struct{}),
		subscribing: map[string]bool{},
		stop:        make(chan struct{}),
	}
	n.loadPeers()
	for _, p := range seedPeers {
		n.addPeer(p)
	}
	if !skipFallbackSeeds { // -isolated skips the baked-in public seeds (vacuum/testnet)
		for _, p := range fallbackSeeds {
			n.addPeer(p)
		}
	}
	// On a new block (tip extension OR sync/reorg adopt): refresh the cached tip
	// snapshot, wake long-poll subscribers, then push to peers.
	chain.OnNewBlock = func(b *core.Block) { n.updateTipSnap(); n.fireNewBlock(); go n.broadcastBlock(b) }
	ti := n.myTip()
	n.tipSnap.Store(&ti) // seed the snapshot so /p2p/tip answers before the first adopt
	return n
}

// updateTipSnap refreshes the lock-free /p2p/tip snapshot. Called from OnNewBlock
// (outside the chain lock) so the hottest peer poll never blocks on c.mu.
func (n *Node) updateTipSnap() { ti := n.myTip(); n.tipSnap.Store(&ti) }

// newBlockSignal returns a channel closed when the next block is adopted; a
// long-poll subscriber selects on it to be woken the instant a block arrives.
func (n *Node) newBlockSignal() <-chan struct{} {
	n.notifyMu.Lock()
	defer n.notifyMu.Unlock()
	if n.notifyCh == nil {
		n.notifyCh = make(chan struct{})
	}
	return n.notifyCh
}

// fireNewBlock wakes every waiting long-poll subscriber (close + replace).
func (n *Node) fireNewBlock() {
	n.notifyMu.Lock()
	if n.notifyCh != nil {
		close(n.notifyCh)
	}
	n.notifyCh = make(chan struct{})
	n.notifyMu.Unlock()
}

// ------------------------------------------------------------------ peers

func (n *Node) peersFile() string { return filepath.Join(n.dataDir, "peers.json") }

func (n *Node) loadPeers() {
	raw, err := os.ReadFile(n.peersFile())
	if err != nil {
		return
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		for _, p := range list {
			n.addPeer(p)
		}
	}
}

func (n *Node) savePeers() {
	list := n.peerList()
	raw, _ := json.Marshal(list)
	_ = os.WriteFile(n.peersFile(), raw, 0o644)
}

func (n *Node) addPeer(peerURL string) {
	peerURL = strings.TrimRight(strings.TrimSpace(peerURL), "/")
	if peerURL == "" || !strings.HasPrefix(peerURL, "http") {
		return
	}
	if n.publicURL != "" && peerURL == n.publicURL {
		return
	}
	// SSRF guard: a peer URL arrives unauthenticated via the X-Cerebra-Peer
	// header, and the node will later issue requests to it during sync/discovery.
	// Reject loopback/private/link-local literals so the node can't be aimed at
	// internal services (e.g. cloud metadata 169.254.169.254) or used as a relay.
	if !peerHostAllowed(peerURL) {
		return
	}
	n.peersMu.Lock()
	if _, ok := n.peers[peerURL]; !ok && len(n.peers) < 64 {
		n.peers[peerURL] = time.Time{}
	}
	n.peersMu.Unlock()
}

// safePeerClient builds the HTTP client used for ALL outbound peer requests. Its
// dialer re-checks the *resolved* IP at connect time and refuses to connect to
// loopback/private/link-local/unspecified addresses. This is the authoritative
// SSRF defense: peerHostAllowed only screens IP-literal URLs, so a hostname that
// resolves (or DNS-rebinds) to 169.254.169.254 / 127.0.0.1 / RFC1918 would slip
// past it — but the dial Control hook here blocks the actual connection.
// safePeerTransport builds the SSRF-guarded transport shared by every outbound
// peer client: the dialer re-checks the resolved IP and refuses loopback/private/
// link-local addresses at connect time.
func safePeerTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("refusing dial to unresolved address %q", address)
			}
			if ipIsOwn(ip) {
				// Never dial ourselves. seed.cereblix.com is a round-robin that
				// lists this node, so a dial-by-hostname can resolve back to our
				// own IP; a self-connection is a pure no-op (same chain + mempool)
				// that only churns connections. Refusing here lets the dialer fall
				// through to the next resolved address (e.g. the other seed), so
				// connectivity is unaffected. Checked before the trusted-subnet
				// exemption so a self-dial over the WG mesh is caught too.
				return fmt.Errorf("refusing dial to own address %s", ip)
			}
			if ipTrusted(ip) {
				return nil // operator-declared trusted subnet (e.g. the WG mesh)
			}
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
				return fmt.Errorf("refusing dial to non-public address %s", ip)
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
	}
}

func safePeerClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: safePeerTransport()}
}

// peerHostAllowed rejects URLs whose host is a loopback/private/link-local IP
// literal or an obvious localhost name. Public hostnames/IPs are allowed.
func peerHostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if h := strings.ToLower(host); h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipTrusted(ip) {
			return true // operator-declared trusted subnet (e.g. the WG mesh)
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}

// trustedNets are operator-declared CIDRs (e.g. a private WireGuard mesh) that
// are EXEMPT from the SSRF guard above, so a node may peer with its own internal
// mesh over RFC1918 addresses for both block sync and tx gossip. Empty by
// default — a node that does not pass -trustedsubnet keeps the full guard, so
// this can never weaken the public network. Written once at startup (before any
// peer I/O) and only read thereafter.
var trustedNets []*net.IPNet

// netGuardMu guards the dialer's set-once globals (trustedNets, ownIPs): they are
// written by SetTrustedSubnets/SetOwnIPs (at startup, and re-set by tests) and READ by
// the dialer Control hook (ipTrusted/ipIsOwn) on EVERY peer dial from background
// goroutines. Set-once in prod so it never races there, but synchronizing makes it
// correct under -race and safe if ever re-set.
var netGuardMu sync.RWMutex

// SetTrustedSubnets parses comma-separated CIDRs into the trusted-peer set,
// skipping invalid entries with a warning. Call once, before node.New.
func SetTrustedSubnets(csv string) {
	var nets []*net.IPNet
	for _, c := range strings.Split(csv, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		_, netw, err := net.ParseCIDR(c)
		if err != nil {
			log.Printf("trustedsubnet: ignoring invalid CIDR %q: %v", c, err)
			continue
		}
		nets = append(nets, netw)
	}
	netGuardMu.Lock()
	trustedNets = nets
	netGuardMu.Unlock()
	if len(nets) > 0 {
		log.Printf("trustedsubnet: SSRF guard exempts operator-declared range(s): %s", csv)
	}
}

// ipTrusted reports whether ip falls inside an operator-declared trusted subnet.
func ipTrusted(ip net.IP) bool {
	netGuardMu.RLock()
	defer netGuardMu.RUnlock()
	for _, netw := range trustedNets {
		if netw.Contains(ip) {
			return true
		}
	}
	return false
}

// ownIPs holds this node's own addresses (every local interface IP plus the
// advertised public host's resolved IPs). A node must never dial itself:
// seed.cereblix.com is a round-robin that lists this node, so a dial by hostname
// can resolve back to our own IP, and the string self-check in addPeer only
// catches the exact -public literal. ipIsOwn catches every other form (hostname,
// WG, loopback) at connect time. Written once at startup, read-only thereafter.
var ownIPs = map[string]bool{}

// SetOwnIPs records this node's own addresses so the dialer can refuse to
// connect to itself. Call once, before node.New. publicURL may be empty.
func SetOwnIPs(publicURL string) {
	m := map[string]bool{}
	add := func(ip net.IP) {
		if ip != nil {
			m[ip.String()] = true
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				add(ipn.IP)
			}
		}
	}
	if publicURL != "" {
		if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
			if ip := net.ParseIP(u.Hostname()); ip != nil {
				add(ip)
			} else if ips, err := net.LookupIP(u.Hostname()); err == nil {
				for _, ip := range ips {
					add(ip)
				}
			}
		}
	}
	netGuardMu.Lock()
	ownIPs = m
	netGuardMu.Unlock()
	log.Printf("self-dial guard: %d own address(es) will be refused as peers", len(m))
}

// ipIsOwn reports whether ip is one of this node's own addresses.
func ipIsOwn(ip net.IP) bool {
	if ip == nil {
		return false
	}
	netGuardMu.RLock()
	defer netGuardMu.RUnlock()
	return ownIPs[ip.String()]
}

func (n *Node) peerList() []string {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()
	out := make([]string, 0, len(n.peers))
	for p := range n.peers {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (n *Node) markPeer(url string, ok bool) {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()
	if ok {
		n.peers[url] = time.Now()
	}
}

// ---- peer health: backoff + eviction + doomed-tip memo (RC4) ----

// isProtectedSeed reports whether p must never be evicted (a baked-in fallback seed
// or a configured -peers seed) so eviction can't strand the node with no bootstrap.
func (n *Node) isProtectedSeed(p string) bool {
	q := strings.TrimRight(p, "/")
	for _, s := range fallbackSeeds {
		if strings.TrimRight(s, "/") == q {
			return true
		}
	}
	for _, s := range n.seeds {
		if strings.TrimRight(s, "/") == q {
			return true
		}
	}
	return false
}

// peerReady reports whether a peer is past its backoff and worth contacting this tick.
func (n *Node) peerReady(p string) bool {
	n.phMu.Lock()
	defer n.phMu.Unlock()
	h := n.ph[p]
	return h == nil || time.Now().After(h.nextTry)
}

// peerResult records a sync attempt's outcome: success clears backoff; failure backs
// off exponentially and, past a threshold, evicts a non-seed peer (re-learnable).
func (n *Node) peerResult(p string, ok bool) {
	n.phMu.Lock()
	h := n.ph[p]
	if h == nil {
		h = &peerHealth{}
		n.ph[p] = h
	}
	if ok {
		h.fails = 0
		h.nextTry = time.Time{}
		n.phMu.Unlock()
		return
	}
	h.fails++
	shift := h.fails
	if shift > 6 {
		shift = 6
	}
	h.nextTry = time.Now().Add(time.Duration(1<<uint(shift)) * syncInterval) // ~6s .. ~3.2min
	evict := h.fails >= 20 && !n.isProtectedSeed(p)
	n.phMu.Unlock()
	if evict {
		n.dropPeer(p)
		log.Printf("peer: evicted unresponsive %s (re-learnable via discovery)", p)
	}
}

// noteDoomed / isDoomed remember a peer tip that core rejected, so we stop re-fetching
// the identical losing chain every tick (cleared implicitly when the peer's tip moves).
func (n *Node) noteDoomed(p, tipHash string) {
	n.phMu.Lock()
	defer n.phMu.Unlock()
	if h := n.ph[p]; h != nil {
		h.lastDoomed = tipHash
	} else {
		n.ph[p] = &peerHealth{lastDoomed: tipHash}
	}
}

func (n *Node) isDoomed(p, tipHash string) bool {
	n.phMu.Lock()
	defer n.phMu.Unlock()
	h := n.ph[p]
	return h != nil && h.lastDoomed != "" && h.lastDoomed == tipHash
}

// resetPeerBackoff clears all backoff (used by the watchdog to force a fresh sync pass).
func (n *Node) resetPeerBackoff() {
	n.phMu.Lock()
	defer n.phMu.Unlock()
	for _, h := range n.ph {
		h.fails = 0
		h.nextTry = time.Time{}
	}
}

// clearDoomed forgets every memoized doomed-tip. WATCHDOG-ONLY: after a long
// behind+stuck stall, re-probe forks we previously rejected in case a concurrent
// catch-up race memoized the WINNING chain by mistake (resetPeerBackoff does not
// touch lastDoomed). MUST NOT be called on the normal sync path — that re-opens the
// RC4 doomed-refetch storm (guarded by sim_test.go scenario 4).
func (n *Node) clearDoomed() {
	n.phMu.Lock()
	defer n.phMu.Unlock()
	for _, h := range n.ph {
		h.lastDoomed = ""
	}
}

// dropPeer removes a peer from the active set + health map (re-learnable via discovery).
func (n *Node) dropPeer(p string) {
	n.peersMu.Lock()
	delete(n.peers, p)
	n.peersMu.Unlock()
	n.phMu.Lock()
	delete(n.ph, p)
	n.phMu.Unlock()
}

// ------------------------------------------------------------ http helpers

func (n *Node) getJSON(url string, out any) error { return n.getJSONWith(n.client, url, out) }

func (n *Node) getJSONWith(cl *http.Client, url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if n.publicURL != "" {
		req.Header.Set("X-Cerebra-Peer", n.publicURL)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (n *Node) postJSON(url string, body any, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.publicURL != "" {
		req.Header.Set("X-Cerebra-Peer", n.publicURL)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ------------------------------------------------------------------- sync

type tipInfo struct {
	Height  uint64 `json:"height"`
	Hash    string `json:"hash"`
	CumWork string `json:"cumwork"` // hex
}

func (n *Node) myTip() tipInfo {
	tip := n.Chain.Tip()
	return tipInfo{
		Height:  tip.Height,
		Hash:    tip.Hash(),
		CumWork: n.Chain.CumWork().Text(16),
	}
}

func (n *Node) SyncLoop() {
	tick := 0
	for {
		select {
		case <-n.stop:
			return
		case <-time.After(syncInterval):
		}
		tick++
		// Block sync runs every interval (must stay fast); peer discovery and
		// checkpoint pulls change rarely, so run them ~10x less often to cut
		// steady-state gossip chatter (O(peers) HTTP requests per tick).
		slow := tick%10 == 0
		// Bounded-concurrency fan-out: skip peers in backoff so a dead/wedged peer can't
		// drain the round; TryAdoptChain still serializes adopts under the chain lock.
		sem := make(chan struct{}, syncConcurrency)
		var wg sync.WaitGroup
		for _, p := range n.peerList() {
			if !n.peerReady(p) {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(p string) {
				defer wg.Done()
				defer func() { <-sem }()
				n.syncWithPeer(p)
				if slow {
					n.fetchCheckpoint(p)
				}
			}(p)
		}
		wg.Wait()
		n.updateTipSnap() // RC6: re-publish the true tip every tick so a missed OnNewBlock callback (e.g. a swallowed commit-path panic) can't strand /p2p/tip + /p2p/subscribe on a stale snapshot.
		n.savePeers()
		if slow {
			n.discoverPeers()
		}
		n.checkConcentration()
	}
}

// tipAhead reports whether a peer's announced tip is worth pulling: strictly more
// cumulative work, or equal work whose tip wins the deterministic tie-break (smaller
// hash). Pure mirror of syncWithPeer's early-return logic so the announce-dedup can
// skip a sync that syncWithPeer would itself bail on.
func tipAhead(cumWork, hash string, ourWork *big.Int, ourHash string) bool {
	if len(cumWork) > 80 { // a 256-bit cumwork is ~64 hex; reject absurd values
		return false
	}
	tw, ok := new(big.Int).SetString(cumWork, 16)
	if !ok {
		return false
	}
	switch tw.Cmp(ourWork) {
	case 1:
		return true // strictly more work
	case 0:
		return hash < ourHash // equal work: only the tie-break winner
	}
	return false // we have at least as much work
}

func (n *Node) peerAhead(tip tipInfo) bool {
	return tipAhead(tip.CumWork, tip.Hash, n.Chain.CumWork(), n.Chain.Tip().Hash())
}

// healthyReject classifies a TryAdoptChain rejection that is NOT the peer's fault: we
// lost a concurrent catch-up race (a winner goroutine already advanced us, so an
// identical/already-held prefix now reads as a deep reorg) or the candidate is a
// genuinely too-deep / equal-work losing fork. The peer stays healthy — no backoff,
// no eviction. Anything else (bad/invalid data, truncation, transient) backs off.
func healthyReject(err error) bool {
	s := err.Error()
	return strings.Contains(s, "lacks sufficient work") || strings.Contains(s, "reorg too deep")
}

func (n *Node) syncWithPeer(peer string) {
	ok := n.syncWithPeerOnce(peer)
	n.peerResult(peer, ok) // backoff/evict an unreachable or flaky peer; on-a-losing-fork counts as ok
}

// syncWithPeerOnce performs one sync attempt and reports whether the peer was
// USABLE this round (reachable + protocol-sane + served what it advertised). A peer
// merely on a losing fork returns true (we memo its doomed tip and stop re-fetching
// it); an unreachable/flaky/bad-data peer returns false (→ exponential backoff).
func (n *Node) syncWithPeerOnce(peer string) (ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("sync: recovered from panic on peer %s: %v", peer, rec)
			ok = false
		}
	}()
	var their tipInfo
	if err := n.getJSON(peer+"/p2p/tip", &their); err != nil {
		return false
	}
	n.markPeer(peer, true)
	if len(their.CumWork) > 80 { // a 256-bit cumwork is ~64 hex; reject absurd values
		return true
	}
	theirWork, okw := new(big.Int).SetString(their.CumWork, 16)
	if !okw {
		return true
	}
	switch theirWork.Cmp(n.Chain.CumWork()) {
	case -1:
		return true // we already have strictly more work
	case 0:
		// Equal cumulative work: pursue ONLY the deterministic tie-break winner
		// (smaller tip hash), mirroring core.TryAdoptChain's tie-break so we never
		// fetch a chain core will reject. Otherwise keep ours and nudge the peer.
		ourTip := n.Chain.Tip()
		if their.Hash == ourTip.Hash() {
			return true
		}
		if their.Hash >= ourTip.Hash() {
			go n.pushTip(peer)
			return true
		}
		log.Printf("fork: peer %s on competing equal-work tip @%d (theirs wins tie-break, adopting)", peer, their.Height)
	}
	// Don't re-fetch a tip we already learned this peer can't make us adopt; the memo
	// self-clears once the peer's tip advances (a different hash is not doomed).
	if n.isDoomed(peer, their.Hash) {
		return true
	}
	// Find the common ancestor (binary search over heights).
	ourH := n.Chain.Height()
	lo, hi := uint64(0), ourH
	if their.Height < hi {
		hi = their.Height
	}
	anc := uint64(0)
	for lo <= hi {
		mid := (lo + hi) / 2
		var hr struct {
			Hash string `json:"hash"`
		}
		if err := n.getJSON(fmt.Sprintf("%s/p2p/hash?h=%d", peer, mid), &hr); err != nil {
			return false // peer went flaky mid-search → back off; another peer covers it
		}
		ours := n.Chain.BlockAt(mid)
		if ours != nil && ours.Hash() == hr.Hash {
			anc = mid
			lo = mid + 1
		} else {
			if mid == 0 {
				return true // genesis mismatch: not our network (reachable though)
			}
			hi = mid - 1
		}
	}
	log.Printf("sync: peer %s ahead (h=%d vs %d), fetching from %d", peer, their.Height, ourH, anc+1)
	isReorg := anc < ourH
	if isReorg {
		log.Printf("fork: reorg depth %d from peer %s (ancestor %d, our tip %d, theirs %d)", ourH-anc, peer, anc, ourH, their.Height)
	}
	// A concurrent fan-out round may have already advanced us STRICTLY PAST this peer's
	// work between the tip read and here. If so, pull nothing: avoids a wasted fetch AND
	// a spurious "reorg too deep" on blocks we now hold. Use '> 0' (NOT '>= 0'): an
	// EQUAL-work peer must still be pursued — that is the same-height tie-break path
	// (RC1); the redundant case where we already adopted its tip is caught by
	// healthyReject when TryAdoptChain reports it as already-held.
	if n.Chain.CumWork().Cmp(theirWork) > 0 {
		return true
	}
	var pending []*core.Block
	from := anc + 1
	truncated := false
	// adopt verifies PoW off-lock, then applies under the chain lock. `complete` = we
	// hold the whole advertised candidate, so a "lacks sufficient work" rejection is a
	// genuine losing fork (memoize it); a partial/other rejection is not.
	adopt := func(complete bool) error {
		n.Chain.PreVerifyPoW(anc+1, pending)  // memory-hard PoW off-lock
		n.Chain.PreVerifySigs(anc+1, pending) // per-tx ed25519 off-lock
		if err := n.Chain.TryAdoptChain(anc+1, pending); err != nil {
			log.Printf("sync: adopt failed: %v", err)
			if complete && strings.Contains(err.Error(), "lacks sufficient work") {
				n.noteDoomed(peer, their.Hash)
			}
			return err
		}
		anc = n.Chain.Height()
		pending = nil
		return nil
	}
	for {
		var batch []*core.Block
		url := fmt.Sprintf("%s/p2p/blocks?from=%d&count=%d", peer, from, batchBlocks)
		if err := n.getJSON(url, &batch); err != nil || len(batch) == 0 {
			if from <= their.Height {
				truncated = true // peer didn't serve the full chain it advertised
			}
			break
		}
		pending = append(pending, batch...)
		from += uint64(len(batch))
		complete := from > their.Height
		// Chunk only pure EXTENSIONS; a reorg candidate must be adopted whole (it has to
		// outweigh our chain as a unit), so accumulate it until complete.
		if complete || (!isReorg && len(pending) >= adoptChunk) {
			if err := adopt(complete); err != nil {
				return healthyReject(err) // raced/too-deep/losing-fork = peer healthy; only bad data backs off
			}
			if complete {
				break
			}
		}
	}
	if len(pending) > 0 {
		if err := adopt(!truncated); err != nil {
			return healthyReject(err)
		}
	}
	if truncated {
		return false // couldn't pull the full advertised chain → treat as flaky this round
	}
	log.Printf("sync: now at height %d", n.Chain.Height())
	return true
}

// fetchCheckpoint pulls a peer's authority checkpoint, verifies its signature
// against the hardcoded authority key, and enforces it if it matches our chain.
func (n *Node) fetchCheckpoint(peer string) {
	var cp core.Checkpoint
	if err := n.getJSON(peer+"/p2p/checkpoint", &cp); err != nil {
		return
	}
	if !cp.Verify() {
		return
	}
	// Retain the signature-verified checkpoint as a deep-reorg recovery ANCHOR even
	// when ApplyCheckpoint returns false (we're on a wrong fork so we hold no matching
	// block). It is NOT written into the enforced Checkpoints set; it only lets a
	// >maxreorg-behind honest node re-converge to the authority chain once the gated
	// deep-recovery rule (Stage 2) activates. Forgery-proof: signature already verified.
	n.Chain.SetAuthorityAnchor(cp)
	if n.Chain.ApplyCheckpoint(cp) {
		n.cpMu.Lock()
		isNew := cp.Height > n.checkpoint.Height
		if cp.Height >= n.checkpoint.Height {
			n.checkpoint = cp
		}
		n.cpMu.Unlock()
		// Only log when the enforced checkpoint actually advances, otherwise every
		// poll spams the same line.
		if isNew {
			log.Printf("checkpoint: enforcing authority checkpoint at height %d", cp.Height)
		}
	}
}

func (n *Node) discoverPeers() {
	for _, p := range n.peerList() {
		var list []string
		if err := n.getJSON(p+"/p2p/peers", &list); err == nil {
			for _, u := range list {
				n.addPeer(u)
			}
		}
	}
}

func (n *Node) broadcastBlock(b *core.Block) {
	for _, p := range n.peerList() {
		go func(peer string) {
			var resp map[string]string
			_ = n.postJSON(peer+"/p2p/block", b, &resp)
		}(p)
	}
}

// pushTip posts our current tip to a peer whose tip differs, nudging it to
// re-evaluate now instead of on its next poll: if we win the equal-work
// tie-break the peer adopts our tip; otherwise its /p2p/block returns "not
// extending tip" and it syncs back from us. Best-effort.
func (n *Node) pushTip(peer string) {
	tip := n.Chain.Tip()
	var resp map[string]string
	_ = n.postJSON(peer+"/p2p/block", tip, &resp)
}

// checkConcentration logs a 51%-watch warning (with hysteresis) when a single
// address has mined a majority of recent blocks. Observability only.
func (n *Node) checkConcentration() {
	top, _ := n.Chain.RecentCoinbaseShare(100)
	if top >= 0.5 && !n.concentrationHigh {
		n.concentrationHigh = true
		log.Printf("hashrate: WARNING one address mined %.0f%% of the last 100 blocks (51%%-watch)", top*100)
	} else if top < 0.45 && n.concentrationHigh {
		n.concentrationHigh = false
		log.Printf("hashrate: concentration eased below 45%% of the last 100 blocks")
	}
}

func (n *Node) broadcastTx(t *core.Tx) { n.broadcastTxExcept(t, "") }

// broadcastTxExcept floods a tx to every peer except `exclude` (the peer we
// received it from). Multi-hop gossip: a receiver that accepts a NEW tx re-floods
// it in turn, so it reaches block producers more than one hop away. Dedup in
// AddTx (a tx already in the mempool is rejected) stops the flood once everyone
// has it - no loops.
func (n *Node) broadcastTxExcept(t *core.Tx, exclude string) {
	for _, p := range n.peerList() {
		if p == exclude {
			continue
		}
		go func(peer string) {
			_ = n.postJSON(peer+"/p2p/tx", t, nil)
		}(p)
	}
}

// rebroadcastLoop re-floods our unconfirmed mempool to peers periodically. The
// backstop: a tx submitted while we had no live peers (e.g. right after a
// restart) still propagates once peers connect, and any producer that missed the
// initial flood eventually receives it. Cheap - peers that already hold a tx
// dup-reject it and don't re-flood.
func (n *Node) rebroadcastLoop() {
	for {
		select {
		case <-n.stop:
			return
		case <-time.After(rebroadcastInterval):
		}
		for _, t := range n.Chain.MempoolTxs() {
			n.broadcastTx(t)
		}
	}
}

// subscribeManager keeps one long-poll subscription open to each known peer so
// new blocks arrive by push over OUR outbound connection within milliseconds,
// the way a NAT node stays current in Bitcoin without accepting inbound. Peers
// that don't expose /p2p/subscribe (older nodes) are left to the periodic
// SyncLoop - fully backward compatible.
func (n *Node) subscribeManager() {
	for {
		select {
		case <-n.stop:
			return
		case <-time.After(5 * time.Second):
		}
		for _, p := range n.peerList() {
			n.subMu.Lock()
			if !n.subscribing[p] {
				n.subscribing[p] = true
				go n.subscribeLoop(p)
			}
			n.subMu.Unlock()
		}
	}
}

func (n *Node) subscribeLoop(peer string) {
	defer func() {
		n.subMu.Lock()
		delete(n.subscribing, peer)
		n.subMu.Unlock()
		if rec := recover(); rec != nil {
			log.Printf("subscribe: recovered on peer %s: %v", peer, rec)
		}
	}()
	misses := 0
	for {
		select {
		case <-n.stop:
			return
		default:
		}
		n.peersMu.Lock()
		_, known := n.peers[peer]
		n.peersMu.Unlock()
		if !known {
			return
		}
		var tip tipInfo
		if err := n.getJSONWith(n.subClient, peer+"/p2p/subscribe", &tip); err != nil {
			if strings.Contains(err.Error(), "http 404") {
				return // old node without /p2p/subscribe; SyncLoop still polls it
			}
			if misses++; misses >= 4 {
				return // unreliable; SyncLoop covers it and the manager retries later
			}
			time.Sleep(5 * time.Second)
			continue
		}
		misses = 0
		// Dedup the N-way announce storm: /p2p/subscribe already gave us the peer's
		// tip, so only pull when it is actually ahead. Otherwise every peer announcing
		// the same new block triggers a redundant syncWithPeer round-trip after the
		// first one already adopted it. SyncLoop still polls as a backstop, and the
		// rare equal-work-fork pushTip is covered there.
		if n.peerAhead(tip) {
			n.syncWithPeer(peer) // a block was announced and the peer is ahead: pull & adopt
		}
	}
}

// ------------------------------------------------------- per-IP rate limiter

// rateLimiter is a token-bucket limiter keyed by client IP. It fronts the
// unauthenticated, internet-exposed P2P port so a single source cannot flood
// the node with expensive PoW-verify (/p2p/block) or sync requests. Limits are
// generous enough for honest peers syncing in 200-block batches.
type rateLimiter struct {
	mu    sync.Mutex
	b     map[string]*tokenBucket
	rate  float64 // tokens refilled per second
	burst float64 // bucket capacity
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// rlMaxBuckets caps the limiter's memory footprint.
const rlMaxBuckets = 8192

func newRateLimiter(rate, burst float64) *rateLimiter {
	return &rateLimiter{b: map[string]*tokenBucket{}, rate: rate, burst: burst}
}

// gcLocked frees memory while preserving active throttle state. It first drops
// buckets that have fully refilled (idle — recreating one later yields the same
// full burst, so no limit is lost). If still over cap (a genuine large-scale
// distinct-IP flood), it trims arbitrary entries down to the cap. Unlike the
// previous full-map wipe, honest peers currently being throttled keep their
// depleted buckets. Caller must hold rl.mu.
func (rl *rateLimiter) gcLocked(now time.Time) {
	for ip, tb := range rl.b {
		if tb.tokens+now.Sub(tb.last).Seconds()*rl.rate >= rl.burst {
			delete(rl.b, ip)
		}
	}
	for ip := range rl.b {
		if len(rl.b) <= rlMaxBuckets {
			break
		}
		delete(rl.b, ip)
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	tb := rl.b[ip]
	if tb == nil {
		// Bound memory without throwing away active throttle state: a flood of
		// spoofed-source IPs must not reset everyone's bucket to full burst (the
		// old full-map wipe did exactly that). Evict idle/expired buckets first.
		if len(rl.b) >= rlMaxBuckets {
			rl.gcLocked(now)
		}
		tb = &tokenBucket{tokens: rl.burst, last: now}
		rl.b[ip] = tb
	}
	tb.tokens += now.Sub(tb.last).Seconds() * rl.rate
	if tb.tokens > rl.burst {
		tb.tokens = rl.burst
	}
	tb.last = now
	if tb.tokens < 1 {
		return false
	}
	tb.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (rl *rateLimiter) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			writeErr(w, 429, "rate limit exceeded")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// -------------------------------------------------------------- p2p server

func (n *Node) P2PHandler() http.Handler {
	mux := http.NewServeMux()
	reg := func(w http.ResponseWriter, r *http.Request) {
		if u := r.Header.Get("X-Cerebra-Peer"); u != "" {
			n.addPeer(u)
		}
	}
	mux.HandleFunc("/p2p/tip", func(w http.ResponseWriter, r *http.Request) {
		reg(w, r)
		if ti := n.tipSnap.Load(); ti != nil {
			writeJSON(w, *ti) // lock-free: the hottest peer poll never blocks on the chain lock
			return
		}
		writeJSON(w, n.myTip())
	})
	mux.HandleFunc("/p2p/hash", func(w http.ResponseWriter, r *http.Request) {
		h, _ := strconv.ParseUint(r.URL.Query().Get("h"), 10, 64)
		b := n.Chain.BlockAt(h)
		if b == nil {
			writeErr(w, 404, "no such height")
			return
		}
		writeJSON(w, map[string]string{"hash": b.Hash()})
	})
	mux.HandleFunc("/p2p/blocks", func(w http.ResponseWriter, r *http.Request) {
		from, _ := strconv.ParseUint(r.URL.Query().Get("from"), 10, 64)
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		if count <= 0 || count > batchBlocks {
			count = batchBlocks
		}
		writeJSON(w, n.Chain.Blocks(from, count))
	})
	mux.HandleFunc("/p2p/block", func(w http.ResponseWriter, r *http.Request) {
		reg(w, r)
		var b core.Block
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		err := n.Chain.AddBlock(&b)
		if err == nil {
			log.Printf("p2p: accepted block %d %s", b.Height, b.Hash()[:16])
			writeJSON(w, map[string]string{"result": "accepted"})
			return
		}
		if errors.Is(err, errNotTip) || strings.Contains(err.Error(), "not extending tip") {
			// Maybe a longer chain exists; sync will pick it up.
			if u := r.Header.Get("X-Cerebra-Peer"); u != "" {
				go n.syncWithPeer(strings.TrimRight(u, "/"))
			}
		}
		writeErr(w, 400, err.Error())
	})
	mux.HandleFunc("/p2p/tx", func(w http.ResponseWriter, r *http.Request) {
		var t core.Tx
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		if err := n.Chain.AddTx(&t); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// Accepted a NEW (or replacement) tx -> re-flood to our other peers so it
		// reaches producers beyond one hop. Exclude the sender; dedup stops loops.
		from := strings.TrimRight(r.Header.Get("X-Cerebra-Peer"), "/")
		go n.broadcastTxExcept(&t, from)
		writeJSON(w, map[string]string{"result": "accepted"})
	})
	mux.HandleFunc("/p2p/peers", func(w http.ResponseWriter, r *http.Request) {
		reg(w, r)
		writeJSON(w, n.peerList())
	})
	// Long-poll: hold the request until a new block is adopted (push), or a short
	// timeout after which the caller re-subscribes. This lets a node behind NAT
	// receive blocks instantly over the connection IT opened, without being
	// publicly reachable - the same property that keeps NAT nodes current in
	// Bitcoin. Older peers simply don't call this; nothing breaks.
	mux.HandleFunc("/p2p/subscribe", func(w http.ResponseWriter, r *http.Request) {
		reg(w, r)
		select {
		case <-n.newBlockSignal():
		case <-time.After(subscribeHold):
		case <-r.Context().Done():
			return
		}
		if ti := n.tipSnap.Load(); ti != nil {
			writeJSON(w, *ti) // RC6: lock-free, same path as /p2p/tip — a woken subscriber never blocks on the chain lock (myTip's RLock), so a catch-up holding c.mu can't pile these handlers into CLOSE_WAIT.
			return
		}
		writeJSON(w, n.myTip())
	})
	// Serve the latest authority checkpoint so peers can pull and enforce it.
	// Receivers verify the signature against the hardcoded authority key, so a
	// relaying peer cannot forge one.
	mux.HandleFunc("/p2p/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		n.cpMu.Lock()
		cp := n.checkpoint
		n.cpMu.Unlock()
		if cp.Hash == "" {
			writeErr(w, 404, "no checkpoint")
			return
		}
		writeJSON(w, cp)
	})
	return mux
}

var errNotTip = errors.New("not extending tip")

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0 && len(s) <= 18
}

// -------------------------------------------------------------- rpc server

func (n *Node) RPCHandler() http.Handler {
	mux := http.NewServeMux()
	h := func(path string, fn http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == "OPTIONS" {
				return
			}
			fn(w, r)
		})
	}

	h("/api/status", func(w http.ResponseWriter, r *http.Request) {
		tip := n.Chain.Tip()
		tgt, _ := tip.TargetInt()
		diff := core.WorkOf(tgt)
		// Network hashrate = the work actually done over a recent window divided by
		// that window's REAL elapsed time (realized rate) - the same quantity a pool
		// or explorer reports, derived purely from on-chain data (a node never sees
		// off-chain shares). Dividing by the REAL time (not the 60s target) makes it
		// track the true rate immediately instead of lagging behind LWMA's difficulty
		// retarget (the old `work/target` form read low whenever hashrate had just
		// risen, even while blocks were coming fast). A ~30-block window smooths
		// single-block Poisson luck; a genuine multi-minute stall still pulls it down.
		// DISPLAY-ONLY: difficulty (LWMA) + all consensus are computed elsewhere and
		// are completely unaffected by this number.
		var hashrate float64
		hgt := n.Chain.Height()
		now := uint64(time.Now().Unix())
		if hgt >= 1 {
			const win = 30
			w0 := uint64(win)
			if hgt < w0 {
				w0 = hgt
			}
			work := new(big.Int)
			for i := hgt - w0 + 1; i <= hgt; i++ {
				t, _ := n.Chain.BlockAt(i).TargetInt()
				work.Add(work, core.WorkOf(t))
			}
			// Real time to mine those w0 blocks = tip.Time - (parent of the first).Time.
			elapsed := float64(tip.Time) - float64(n.Chain.BlockAt(hgt-w0).Time)
			// A genuine ongoing outage (current gap > 10x target) extends the window to
			// 'now' so the rate visibly drops during a real stall, not only on the next
			// block - aligned with the LWMA solvetime clamp (10*T).
			if sinceTip := float64(now) - float64(tip.Time); sinceTip > float64(core.BlockTargetSpacing)*10 {
				elapsed += sinceTip
			}
			if elapsed < 1 {
				elapsed = 1
			}
			workF, _ := new(big.Float).SetInt(work).Float64()
			hashrate = workF / elapsed
		}
		blockAge := int64(now) - int64(tip.Time)
		if blockAge < 0 {
			blockAge = 0
		}
		_, epoch := n.Chain.EpochSeedForNext()
		v4sig, v4req, v4win, anchorH := n.Chain.ConsensusStatus()
		writeJSON(w, map[string]any{
			"height":            tip.Height,
			"tip":               tip.Hash(),
			"time":              tip.Time,
			"target":            tip.Target,
			"difficulty":        diff.String(),
			"supply":            n.Chain.Supply(),
			"mempool":           n.Chain.MempoolLen(),
			"peers":             len(n.peerList()),
			"epoch":             epoch,
			"reward":            core.BlockSubsidy(tip.Height + 1),
			"hashrate":          hashrate,
			"block_age":         blockAge,
			"now":               now,
			"fee_suggested":     n.Chain.SuggestedFee(),
			"fee_floor":         n.Chain.FeeFloor(),
			"node_version":      n.Version,
			"consensus_version": core.NodeConsensusVersion,
			"v4_signal":         v4sig,   // blocks in the last v4_window signaling consensus v4 (deep-recovery hardfork gate)
			"v4_required":       v4req,   // gate activates once v4_signal reaches this (95% of the window)
			"v4_window":         v4win,   // size of the v4 signal window (blocks)
			"authority_anchor":  anchorH, // height of the retained signed deep-recovery anchor (0 = none)
			"chain_id":          core.ChainID,
			"chain_id_height":   core.ChainIDHeight,
		})
	})

	h("/api/balance", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("addr")
		if !core.ValidAddr(addr) {
			writeErr(w, 400, "bad address")
			return
		}
		acc := n.Chain.Account(addr)
		recv, mined, sent, txn := n.Chain.AddrTotals(addr)
		writeJSON(w, map[string]any{"address": addr, "balance": acc.Balance, "nonce": acc.Nonce,
			"spendable": n.Chain.SpendableBalance(addr),
			"received":  recv, "mined": mined, "sent": sent, "txn": txn})
	})

	h("/api/history", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("addr")
		if !core.ValidAddr(addr) {
			writeErr(w, 400, "bad address")
			return
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		writeJSON(w, n.Chain.History(addr, limit, offset))
	})

	h("/api/blocks", func(w http.ResponseWriter, r *http.Request) {
		last, _ := strconv.Atoi(r.URL.Query().Get("last"))
		if last <= 0 || last > 100 {
			last = 15
		}
		// `before` pages backwards: return up to `last` blocks with height < before
		// (newest-first). Omit it for the latest blocks. The total is status.height+1.
		top := n.Chain.Height()
		if bs := r.URL.Query().Get("before"); bs != "" {
			bv, err := strconv.ParseUint(bs, 10, 64)
			if err != nil || bv == 0 {
				writeJSON(w, []map[string]any{})
				return
			}
			if bv-1 < top {
				top = bv - 1
			}
		}
		from := uint64(0)
		if uint64(last) <= top {
			from = top - uint64(last) + 1
		}
		blocks := n.Chain.Blocks(from, int(top-from+1))
		out := make([]map[string]any, 0, len(blocks))
		for i := len(blocks) - 1; i >= 0; i-- {
			b := blocks[i]
			var miner string
			var reward uint64
			if len(b.Txs) > 0 { // guard: a malformed/empty-tx block must not panic the RPC
				miner = b.Txs[0].To
				reward = b.Txs[0].Amount
			}
			out = append(out, map[string]any{
				"height": b.Height, "hash": b.Hash(), "time": b.Time,
				"txs": len(b.Txs), "miner": miner, "reward": reward,
			})
		}
		writeJSON(w, out)
	})

	h("/api/block", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if hs := q.Get("h"); hs != "" {
			hgt, err := strconv.ParseUint(hs, 10, 64)
			if err != nil {
				writeErr(w, 400, "bad height")
				return
			}
			b := n.Chain.BlockAt(hgt)
			if b == nil {
				writeErr(w, 404, "not found")
				return
			}
			writeJSON(w, b)
			return
		}
		if hash := q.Get("hash"); hash != "" {
			b := n.Chain.BlockByHash(hash)
			if b == nil {
				writeErr(w, 404, "not found")
				return
			}
			writeJSON(w, b)
			return
		}
		writeErr(w, 400, "need h= or hash=")
	})

	h("/api/mempool", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, n.Chain.MempoolTxs())
	})

	h("/api/tx", func(w http.ResponseWriter, r *http.Request) {
		// GET /api/tx?id=<txid> looks up a transaction (explorer).
		if r.Method == "GET" {
			id := r.URL.Query().Get("id")
			if len(id) != 64 {
				writeErr(w, 400, "need id=<64 hex>")
				return
			}
			loc := n.Chain.FindTx(id)
			if !loc.Found {
				writeErr(w, 404, "transaction not found")
				return
			}
			writeJSON(w, loc)
			return
		}
		if r.Method != "POST" {
			writeErr(w, 405, "GET (lookup) or POST (submit) only")
			return
		}
		var t core.Tx
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		if err := n.Chain.AddTx(&t); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		go n.broadcastTx(&t)
		writeJSON(w, map[string]string{"result": "accepted", "txid": t.ID()})
	})

	h("/api/mined", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("addr")
		if !core.ValidAddr(addr) {
			writeErr(w, 400, "bad address")
			return
		}
		writeJSON(w, map[string]any{"address": addr, "blocks": n.Chain.MinedBlocks(addr)})
	})

	h("/api/richlist", func(w http.ResponseWriter, r *http.Request) {
		n2, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n2 <= 0 || n2 > 200 {
			n2 = 25
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		writeJSON(w, n.Chain.RichList(n2, offset))
	})

	// /api/search?q= classifies a query and points the explorer at the right view.
	h("/api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		switch {
		case q == "":
			writeErr(w, 400, "empty query")
		case core.ValidAddr(q):
			writeJSON(w, map[string]string{"type": "address", "value": q})
		case isAllDigits(q):
			hgt, _ := strconv.ParseUint(q, 10, 64)
			if n.Chain.BlockAt(hgt) == nil {
				writeErr(w, 404, "no block at that height")
				return
			}
			writeJSON(w, map[string]any{"type": "block", "height": hgt})
		case len(q) == 64 && isHex(q):
			if b := n.Chain.BlockByHash(q); b != nil {
				writeJSON(w, map[string]any{"type": "block", "height": b.Height})
				return
			}
			if loc := n.Chain.FindTx(q); loc.Found {
				writeJSON(w, map[string]string{"type": "tx", "value": q})
				return
			}
			writeErr(w, 404, "no block or transaction with that hash")
		default:
			writeErr(w, 400, "unrecognized query (height, block hash, txid, or crb1 address)")
		}
	})

	h("/api/getwork", func(w http.ResponseWriter, r *http.Request) {
		addr := r.URL.Query().Get("addr")
		tmpl, id, err := n.getTemplate(addr)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		seed, epoch := n.Chain.EpochSeedForNext()
		writeJSON(w, map[string]any{
			"id":     id,
			"header": hex.EncodeToString(tmpl.HeaderBytes()),
			"target": tmpl.Target,
			"seed":   hex.EncodeToString(seed),
			"epoch":  epoch,
			"height": tmpl.Height,
			"ntx":    len(tmpl.Txs), // coinbase + body txs in this job (observability; clients ignore extras)
		})
	})

	h("/api/submitwork", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeErr(w, 405, "POST only")
			return
		}
		var req struct {
			ID    string          `json:"id"`
			Nonce json.RawMessage `json:"nonce"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		// Accept the nonce as a JSON number OR a quoted string: a 64-bit nonce
		// exceeds JS's 2^53 safe-integer range, so the browser miner sends it as
		// a string; the native miner sends a number.
		nonce, perr := strconv.ParseUint(strings.Trim(string(req.Nonce), "\""), 10, 64)
		if perr != nil {
			writeErr(w, 400, "bad nonce")
			return
		}
		n.tmplMu.Lock()
		e := n.templates[req.ID]
		n.tmplMu.Unlock()
		if e == nil {
			writeErr(w, 404, "stale or unknown work id")
			return
		}
		b := *e.blk
		b.Nonce = nonce
		if err := n.Chain.AddBlock(&b); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		log.Printf("miner: external miner found block %d %s", b.Height, b.Hash()[:16])
		writeJSON(w, map[string]string{"result": "accepted", "hash": b.Hash()})
	})

	// Serve the latest authority-signed upgrade manifest so peers/the website can
	// mirror it where GitHub is blocked. Receivers re-verify the signature.
	h("/api/upgrade", func(w http.ResponseWriter, r *http.Request) {
		n.upgMu.RLock()
		m := n.upg
		n.upgMu.RUnlock()
		if m == nil {
			writeErr(w, 404, "no upgrade manifest")
			return
		}
		writeJSON(w, m)
	})

	h("/api/params", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"coin":             "Cereblix",
			"ticker":           "CRB",
			"unit":             core.CoinUnit,
			"block_time":       core.BlockTargetSpacing,
			"halving_interval": core.HalvingInterval,
			"epoch_length":     core.EpochLength,
			"initial_reward":   core.InitialReward,
			"max_supply":       uint64(core.InitialReward) * core.HalvingInterval * 2,
			"algo":             "NeuroMorph v1",
		})
	})

	// /api/checkpoint: POST (localhost, from the authority signing tool) pushes a
	// signed checkpoint; GET returns the one we currently hold.
	h("/api/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			// Defense in depth: only the local authority signing tool may POST
			// here. The signature check below already makes forgery impossible,
			// but enforcing loopback means even a misconfigured Apache that
			// proxied this path can't let a remote caller reach it.
			if ip := net.ParseIP(clientIP(r)); ip == nil || !ip.IsLoopback() {
				writeErr(w, 403, "checkpoint POST is localhost-only")
				return
			}
			var cp core.Checkpoint
			if err := json.NewDecoder(r.Body).Decode(&cp); err != nil {
				writeErr(w, 400, "bad json")
				return
			}
			if !cp.Verify() {
				writeErr(w, 400, "bad checkpoint signature")
				return
			}
			n.Chain.ApplyCheckpoint(cp)
			n.cpMu.Lock()
			if cp.Height >= n.checkpoint.Height {
				n.checkpoint = cp
			}
			n.cpMu.Unlock()
			writeJSON(w, map[string]any{"result": "accepted", "height": cp.Height})
			return
		}
		n.cpMu.Lock()
		cp := n.checkpoint
		n.cpMu.Unlock()
		writeJSON(w, cp)
	})

	return mux
}

// getTemplate returns the current mining job for `addr` and its work id.
//
// Rather than freeze one template per tip (which starved blocks of every transaction
// that arrived after the tip changed), the job is refreshed from the live mempool at
// most every templateRefresh - the same few-second cache Bitcoin's getblocktemplate
// uses. Each refresh that actually changes the block body is published under a NEW
// work id; older same-tip jobs stay in the map so a miner that already solved one is
// still accepted on submit (no thrown-away work). Because submit reconstructs the
// exact job by id, a moving timestamp never desyncs a miner - which is what the old
// per-tip freeze was guarding against, now handled correctly.
func (n *Node) getTemplate(addr string) (*core.Block, string, error) {
	if !core.ValidAddr(addr) {
		return nil, "", errors.New("bad or missing addr")
	}
	const maxTemplates = 512 // backstop against /api/getwork spam with many addresses
	tip := n.Chain.Tip().Hash()
	key := tip + "|" + addr

	n.tmplMu.Lock()
	defer n.tmplMu.Unlock()

	// Current job still fresh -> serve it unchanged (stable + costs nothing).
	if id := n.tmplLatest[key]; id != "" {
		if e := n.templates[id]; e != nil && time.Since(e.born) < templateRefresh {
			return e.blk, id, nil
		}
	}

	// Rebuild from the live mempool (current txs + a fresh timestamp).
	fresh, err := n.Chain.BuildTemplate(addr)
	if err != nil {
		// Transient build error -> keep serving the last good job if we have one.
		if id := n.tmplLatest[key]; id != "" {
			if e := n.templates[id]; e != nil {
				return e.blk, id, nil
			}
		}
		return nil, "", err
	}

	// Evict jobs built on an older tip - a block on them could never be valid now.
	for k, e := range n.templates {
		if e.blk.PrevHash != tip {
			delete(n.templates, k)
		}
	}
	for k := range n.tmplLatest {
		if !strings.HasPrefix(k, tip+"|") {
			delete(n.tmplLatest, k)
		}
	}

	// Body unchanged -> don't mint a new id (it would needlessly invalidate miners'
	// in-flight work); just reset the freshness timer so we recheck after a refresh.
	if id := n.tmplLatest[key]; id != "" {
		if e := n.templates[id]; e != nil && e.blk.TxRoot == fresh.TxRoot {
			e.born = time.Now()
			return e.blk, id, nil
		}
	}

	// Body changed -> publish a new job. Older same-tip jobs stay valid for late
	// submits; they are dropped on the next tip or by the cap below.
	n.tmplSeq++
	id := key + "|" + strconv.FormatUint(n.tmplSeq, 36)
	n.templates[id] = &workTemplate{blk: fresh, born: time.Now()}
	n.tmplLatest[key] = id

	// Hard cap: drop the OLDEST job first so we never evict the freshest work.
	for len(n.templates) > maxTemplates {
		var oldest string
		var born time.Time
		for k, e := range n.templates {
			if oldest == "" || e.born.Before(born) {
				oldest, born = k, e.born
			}
		}
		delete(n.templates, oldest)
	}

	return fresh, id, nil
}

// ------------------------------------------------------------ built-in miner

func (n *Node) Mine(threads int, coinbase string) {
	log.Printf("miner: starting %d threads, paying to %s", threads, coinbase)
	for i := 0; i < threads; i++ {
		go n.mineWorker(uint64(i), coinbase)
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		last := uint64(0)
		for range t.C {
			cur := n.hashCount.Load()
			log.Printf("miner: %.1f H/s (height %d)", float64(cur-last)/30.0, n.Chain.Height())
			last = cur
		}
	}()
}

func (n *Node) mineWorker(id uint64, coinbase string) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("miner: worker %d recovered from panic: %v; restarting", id, rec)
			time.Sleep(time.Second)
			go n.mineWorker(id, coinbase)
		}
	}()
	var vm *nm.VM
	var vmEpoch uint64 = ^uint64(0)
	for {
		tmpl, err := n.Chain.BuildTemplate(coinbase)
		if err != nil {
			log.Printf("miner: template error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		seed, epoch := n.Chain.EpochSeedForNext()
		if vm == nil || epoch != vmEpoch {
			vm = nm.NewVM(nm.DeriveParams(seed))
			vmEpoch = epoch
		}
		target, _ := tmpl.TargetInt()
		header := tmpl.HeaderBytes()
		height := tmpl.Height
		prevHash := tmpl.PrevHash
		nonce := id<<56 | uint64(time.Now().UnixNano())&0xFFFFFFFF<<8
		deadline := time.Now().Add(templateMaxAge)
		for time.Now().Before(deadline) {
			putNonce(header, nonce)
			hash := vm.Hash(header, height)
			n.hashCount.Add(1)
			if core.HashMeetsTarget(hash, target) {
				b := *tmpl
				b.Nonce = nonce
				if err := n.Chain.AddBlock(&b); err != nil {
					log.Printf("miner: block rejected: %v", err)
				} else {
					log.Printf("miner: FOUND block %d %s", b.Height, b.Hash()[:16])
				}
				break
			}
			nonce++
			if n.Chain.Tip().Hash() != prevHash {
				break // tip moved, rebuild template
			}
		}
	}
}

func putNonce(header []byte, nonce uint64) {
	for i := 0; i < 8; i++ {
		header[core.NonceOffset+i] = byte(nonce >> (8 * i))
	}
}

// ---------------------------------------------------------------- serving

// maxRequestBytes caps any single request body. Blocks/txs are tiny; this
// stops a peer from exhausting memory with a giant POST.
const maxRequestBytes = 8 << 20 // 8 MiB

// harden wraps a handler with a body-size cap and a panic guard so that no
// single malformed request can crash the node or exhaust its memory.
func harden(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("recovered from handler panic: %v", rec)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
		h.ServeHTTP(w, r)
	})
}

// limitListener bounds the number of simultaneously-open accepted connections.
// Without it, a handler stall - every HTTP handler blocked on the chain RLock
// while a long catch-up holds the write lock - lets accepted-but-unserved sockets
// pile into CLOSE_WAIT until the process hits its file-descriptor ceiling and can
// no longer accept OR dial ("accept4: too many open files"), wedging the node
// until a manual restart. Capping concurrency turns that hard wedge into transient
// backpressure that self-heals the instant the lock frees. (Same idea as
// golang.org/x/net/netutil.LimitListener, inlined to avoid the dependency.)
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// newServer builds an http.Server with timeouts that defeat slow-loris and
// idle-socket exhaustion attacks (ListenAndServe's default has none).
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           harden(h),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
	}
}

func (n *Node) Serve(p2pAddr, rpcAddr string) error {
	errc := make(chan error, 2)
	// Per-IP rate limit on the unauthenticated, internet-exposed P2P port.
	// ~25 req/s/IP, burst 50 - far above honest peer sync, far below a flood.
	p2pRL := newRateLimiter(25, 50)
	// Serve on a connection-capped listener so a handler stall can never exhaust
	// the process FD limit and wedge the node (see limitListener).
	serve := func(name, addr string, h http.Handler, maxConns int) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errc <- fmt.Errorf("%s listen %s: %w", name, addr, err)
			return
		}
		log.Printf("%s listening on %s (max %d conns)", name, addr, maxConns)
		errc <- newServer(addr, h).Serve(newLimitListener(ln, maxConns))
	}
	go serve("p2p", p2pAddr, p2pRL.wrap(n.P2PHandler()), maxConnsP2P)
	go serve("rpc", rpcAddr, n.RPCHandler(), maxConnsRPC)
	go n.SyncLoop()
	go n.subscribeManager()
	go n.rebroadcastLoop()
	go n.livenessWatchdog()
	return <-errc
}

// bestPeerWork polls a bounded sample of peers for their advertised cumulative work
// and returns the maximum seen (0 if none reachable). Used by the watchdog to decide
// whether we are genuinely behind.
func (n *Node) bestPeerWork() *big.Int {
	best := new(big.Int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, syncConcurrency)
	for _, p := range n.peerList() {
		wg.Add(1)
		sem <- struct{}{}
		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()
			var t tipInfo
			if err := n.getJSON(p+"/p2p/tip", &t); err != nil || len(t.CumWork) > 64 { // a real 256-bit cumwork is <=64 hex
				return
			}
			w, ok := new(big.Int).SetString(t.CumWork, 16)
			if !ok {
				return
			}
			mu.Lock()
			if w.Cmp(best) > 0 {
				best.Set(w)
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return best
}

// livenessWatchdog turns "silently stuck" (the limitListener band-aid's failure mode)
// into self-detecting + self-correcting. Every 30s: if peers advertise strictly more
// work AND our height has not advanced for stallWindow, it logs LOUDLY and forces a
// fresh sync pass (clears per-peer backoff). With -stall-restart it escalates to ONE
// controlled exit-for-restart after a longer window (OFF by default). NOTE: nothing in
// selfheal rate-limits this — when enabled the operator MUST run under a supervisor
// crash-loop guard (systemd Restart=on-failure + StartLimitIntervalSec/StartLimitBurst).
func (n *Node) livenessWatchdog() {
	const (
		every       = 30 * time.Second
		stallWindow = 15 * time.Minute
		restartAt   = 20 * time.Minute
	)
	var lastH uint64
	stuckSince := time.Time{}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-n.stop:
			return
		case <-t.C:
		}
		h := n.Chain.Height()
		if h != lastH {
			lastH = h
			stuckSince = time.Time{}
			continue
		}
		if n.bestPeerWork().Cmp(n.Chain.CumWork()) <= 0 {
			stuckSince = time.Time{} // not behind — a flat height is fine (network is just quiet)
			continue
		}
		if stuckSince.IsZero() {
			stuckSince = time.Now()
			continue
		}
		stuck := time.Since(stuckSince)
		if stuck < stallWindow {
			continue
		}
		log.Printf("WATCHDOG: behind peers but height stuck at %d for %s — clearing peer backoff + doomed memo, forcing fresh sync", h, stuck.Round(time.Second))
		n.resetPeerBackoff()
		n.clearDoomed() // RC6: watchdog-only — re-probe doomed forks in case a race memoized the winning tip.
		if n.StallRestart && stuck > restartAt {
			log.Printf("WATCHDOG: still stuck after %s with -stall-restart set — exiting for a clean supervisor restart", stuck.Round(time.Second))
			os.Exit(1)
		}
	}
}
