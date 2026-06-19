// cereblix-pool: a minimal CPU mining pool for Cereblix.
//
// It speaks the SAME getwork/submitwork protocol as the node, so the stock
// cereblix-miner works against it unchanged - point -node at the pool. The pool
// hands out work paying to the pool wallet but at an EASIER "share" target, so
// small miners get steady, low-variance credit even when network difficulty is
// high. Each submitted share is re-verified (NeuroMorph hash). When a share also
// meets the real network target the pool forwards the block to the node; the
// reward (minus a pool fee) is split among miners proportional to their shares
// in that round, then paid out from the pool wallet once it crosses a threshold.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cereblix/core"
	nm "cereblix/neuromorph"
)

var (
	nodeAPI   string
	poolAddr  string
	priv      ed25519.PrivateKey
	feePermil uint64 // pool fee in per-mille (e.g. 10 = 1%)
	shareShift uint
	minPayout uint64
	statePath string
	creditSecret string // shared secret guarding /api/credit (faucet captcha shares)
)

// ---------------------------------------------------------------- work cache

type work struct {
	nodeID      string
	header      []byte
	seed        []byte
	height      uint64
	netTarget   *big.Int
	shareTarget *big.Int
	seen        map[uint64]bool
}

var (
	workMu        sync.Mutex
	curWork       *work
	lastFetch     time.Time
	vmMu          sync.Mutex
	curParams     *nm.Params
	curParamEpoch uint64 = ^uint64(0)
	vmSem         chan struct{}
)

type epochVM struct {
	vm    *nm.VM
	epoch uint64
}

var vmPool sync.Pool

// Per-miner extranonce: a unique 16-bit tag pinned into the top bits of every
// nonce a given address mines. Because those bits are part of the hashed header,
// a valid share is cryptographically bound to one miner — the pool rejects a
// share whose nonce tag doesn't match the extranonce it issued to that address,
// so nobody can submit another miner's solution under their own address (and the
// global nonce dedup no longer collides across miners, since their search spaces
// are disjoint).
var (
	enMu       sync.Mutex
	enAssigned = map[string]uint64{}
	enCounter  uint64
)

func extranonceFor(addr string) uint64 {
	enMu.Lock()
	defer enMu.Unlock()
	if e, ok := enAssigned[addr]; ok {
		return e
	}
	enCounter++
	e := enCounter & 0xFFFF
	if enCounter > 0xFFFF {
		log.Printf("pool: WARNING >65535 distinct miners, extranonce space wrapping")
	}
	enAssigned[addr] = e
	return e
}

// The extranonce map is snapshotted (disk + Postgres in db-mode) and reloaded on startup so a
// restart/failover hands each miner back the SAME extranonce. Without this, a restart reassigns
// extranonces by reconnection order → every miner's in-flight shares fail the share-binding check
// (notbound) and get rejected until the NEXT block makes the stratum bridge push a fresh job with
// the new extranonce — the ~30-60s post-restart "transient". With it, the restart is transparent:
// in-flight shares validate from the first second, no waiting for a block.
type enSnapshot struct {
	Counter  uint64            `json:"c"`
	Assigned map[string]uint64 `json:"a"`
}

func enPath() string { return statePath + ".extranonce" }

func saveExtranonce() {
	enMu.Lock()
	snap := enSnapshot{Counter: enCounter, Assigned: make(map[string]uint64, len(enAssigned))}
	for k, v := range enAssigned {
		snap.Assigned[k] = v
	}
	enMu.Unlock()
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := enPath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, enPath()) // atomic replace
	}
	if dbMode {
		if err := dbSaveExtranonce(string(raw)); err != nil {
			log.Printf("extranonce snapshot: db save: %v", err)
		}
	}
}

// loadExtranonce restores the per-miner extranonce map at startup/failover. In db-mode it reads
// the Postgres snapshot (replicated to the standby by Patroni → a promoted node keeps the map);
// otherwise the on-disk file. Counters only ever move forward, so assignments never collide.
func loadExtranonce() {
	var raw []byte
	if dbMode {
		if s, err := dbLoadExtranonce(); err != nil {
			log.Printf("extranonce snapshot: db load: %v", err)
		} else if s != "" {
			raw = []byte(s)
		}
	}
	if raw == nil {
		raw, _ = os.ReadFile(enPath())
	}
	if len(raw) == 0 {
		return
	}
	var snap enSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		return
	}
	enMu.Lock()
	if snap.Assigned != nil {
		enAssigned = snap.Assigned
	}
	if snap.Counter > enCounter {
		enCounter = snap.Counter // never reissue a lower counter (keep assignments unique)
	}
	n := len(enAssigned)
	enMu.Unlock()
	log.Printf("extranonce: restored %d miner assignments (counter=%d) — a restart/failover keeps each miner's extranonce, so shares are accepted immediately (no post-restart resync wait)", n, snap.Counter)
}

// Rolling log of accepted shares, used to estimate live hashrate (pool-wide and
// per miner) for the public dashboard.
type shareEvent struct {
	t      time.Time
	miner  string
	worker string
	weight float64 // 1 for a real pool-difficulty share; fractional for work-proportional captcha credit
}

var (
	shareMu sync.Mutex
	shareEv []shareEvent
)

// shareEvSnap is the on-disk form of a share event (unexported struct fields can't be
// JSON-marshalled directly). We snapshot the rolling window to disk every 30s and reload
// it on startup so active_miners / pool_hashrate show REAL numbers immediately after a
// restart instead of rebuilding from 0 — that 0→N rebuild was the "pool is dead after a
// reboot" false alarm that triggered every panic-rollback today.
type shareEvSnap struct {
	T int64   `json:"t"` // unix nanos
	M string  `json:"m"` // miner
	K string  `json:"k"` // worker
	W float64 `json:"w"` // weight
}

func shareEvPath() string { return statePath + ".shareev" }

// saveShareEv snapshots the rolling stats window to disk (always — fast same-node restart) and,
// when toDB is set in db-mode, to the Postgres row that Patroni replicates to the standby (so a
// promoted node keeps live active_miners/hashrate instead of rebuilding from zero).
func saveShareEv(toDB bool) {
	shareMu.Lock()
	snap := make([]shareEvSnap, 0, len(shareEv))
	for _, e := range shareEv {
		snap = append(snap, shareEvSnap{e.t.UnixNano(), e.miner, e.worker, e.weight})
	}
	shareMu.Unlock()
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := shareEvPath() + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, shareEvPath()) // atomic replace
	}
	if toDB && dbMode {
		if err := dbSaveShareEv(string(raw)); err != nil {
			log.Printf("shareEv snapshot: db save: %v", err)
		}
	}
}

// loadShareEv restores the stats window at startup/failover — Postgres first (a promoted standby
// keeps live stats), then the local file.
func loadShareEv() {
	var raw []byte
	if dbMode {
		if s, err := dbLoadShareEv(); err == nil && s != "" {
			raw = []byte(s)
		}
	}
	if raw == nil {
		raw, _ = os.ReadFile(shareEvPath())
	}
	if len(raw) == 0 {
		return
	}
	var snap []shareEvSnap
	if json.Unmarshal(raw, &snap) != nil {
		return
	}
	cut := time.Now().Add(-10 * time.Minute)
	shareMu.Lock()
	for _, s := range snap {
		t := time.Unix(0, s.T)
		if t.Before(cut) {
			continue // drop events already outside the live window
		}
		shareEv = append(shareEv, shareEvent{t, s.M, s.K, s.W})
	}
	n := len(shareEv)
	shareMu.Unlock()
	log.Printf("shareEv: restored %d recent share events — active_miners/hashrate survive this restart/failover", n)
}

// sanitizeWorker keeps a short, URL- and work-id-safe worker label (or "").
// Strips the '~' and '|' work-id separators and control chars; caps length.
func sanitizeWorker(s string) string {
	s = strings.TrimSpace(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '~' || r == '|' || r < 0x20 {
			continue
		}
		out = append(out, r)
		if len(out) >= 24 {
			break
		}
	}
	return string(out)
}

func recordShare(miner, worker string) { recordShareW(miner, worker, 1) }

// recordShareW logs a share with an explicit weight (1 = one real pool-difficulty share;
// fractional = work-proportional captcha credit) so the live hashrate display reflects the
// TRUE work each address contributed, not a flat per-event count.
func recordShareW(miner, worker string, weight float64) {
	now := time.Now()
	shareMu.Lock()
	shareEv = append(shareEv, shareEvent{now, miner, worker, weight})
	cut := now.Add(-10 * time.Minute)
	i := 0
	for i < len(shareEv) && shareEv[i].t.Before(cut) {
		i++
	}
	if i > 0 {
		shareEv = shareEv[i:]
	}
	shareMu.Unlock()
}

// hashesPerShare is the expected number of NeuroMorph hashes per accepted share
// (2^256 / shareTarget). hashrate = sharesInWindow * hashesPerShare / windowSecs.
func hashesPerShare() float64 {
	workMu.Lock()
	var stgt *big.Int
	if curWork != nil {
		stgt = curWork.shareTarget
	}
	workMu.Unlock()
	if stgt == nil || stgt.Sign() <= 0 {
		return 0
	}
	max := new(big.Int).Lsh(big.NewInt(1), 256)
	hps, _ := new(big.Float).SetInt(new(big.Int).Div(max, stgt)).Float64()
	return hps
}

// ------------------------------------------------------------- accounting

type state struct {
	mu          sync.Mutex
	Shares      map[string]float64   `json:"-"`        // current round (ephemeral)
	Earned      map[string]uint64    `json:"earned"`   // cumulative credited - SOURCE OF TRUTH
	InFlight    map[string]*inflight `json:"inflight"` // payouts sent, awaiting confirmation
	Owed        map[string]uint64    `json:"owed"`     // DERIVED cache: Earned - on-chain Delivered
	Paid        map[string]uint64    `json:"paid"`     // DERIVED cache: on-chain Delivered
	Found       int                  `json:"found"`    // blocks found by the pool
	RoundShares float64              `json:"-"`        // display only (current round); NOT the payout basis
	ChainHeight uint64               `json:"-"`        // last reconciled chain height
	// PPLNS sliding window = the PAYOUT basis (hop-proof). On a block we pay the last N
	// shares' weight, not the current round, so joining mid-round gives no advantage and
	// pool-hopping is unprofitable. Persisted so a restart never loses miners' recent work.
	PPLNS    []pplnsEntry `json:"pplns"`
	PplnsSum float64      `json:"pplnsSum"`
}

type pplnsEntry struct {
	M string  `json:"m"`
	W float64 `json:"w"`
}

var st = &state{Shares: map[string]float64{}, Earned: map[string]uint64{}, InFlight: map[string]*inflight{}, Owed: map[string]uint64{}, Paid: map[string]uint64{}}

func (s *state) load() {
	if !dbMode {
		if raw, err := os.ReadFile(statePath); err == nil {
			_ = json.Unmarshal(raw, s)
		}
	}
	if s.Owed == nil {
		s.Owed = map[string]uint64{}
	}
	if s.Paid == nil {
		s.Paid = map[string]uint64{}
	}
	if s.Earned == nil {
		s.Earned = map[string]uint64{}
	}
	if s.InFlight == nil {
		s.InFlight = map[string]*inflight{}
	}
	s.Shares = map[string]float64{}
	if dbMode {
		// In HA mode the Earned + PPLNS window + inflight set live in Postgres;
		// reconcile() refreshes the in-memory caches (Earned/Owed/Paid/InFlight) from it.
		return
	}
	// One-time migration to chain-reconciled accounting: cumulative Earned is the
	// old (owed + paid). After this, reconcile() derives Owed/Paid from the chain.
	if len(s.Earned) == 0 && (len(s.Owed) > 0 || len(s.Paid) > 0) {
		for m, v := range s.Owed {
			s.Earned[m] += v
		}
		for m, v := range s.Paid {
			s.Earned[m] += v
		}
		log.Printf("pool: migrated %d miners to chain-reconciled accounting (Earned = owed + paid)", len(s.Earned))
	}
	s.PplnsSum = 0 // recompute from the persisted window (defensive against a drifted cache)
	for _, e := range s.PPLNS {
		s.PplnsSum += e.W
	}
}

func (s *state) save() {
	if dbMode {
		return // state is written through to Postgres at each operation
	}
	raw, _ := json.Marshal(s)
	_ = os.WriteFile(statePath, raw, 0o600)
}

// ------------------------------------------------------------------ node i/o

// nodeClient bounds EVERY call to the node. Without a timeout a hung node endpoint
// (e.g. /mempool or /balance while the node is busy) would block reconcile() and the
// payout loop FOREVER — that is exactly how payouts silently stalled (the leader lease
// was never reached, so it stayed unacquired and nobody paid). A bounded client turns a
// node hang into a logged, recoverable error instead of a permanent freeze.
var nodeClient = &http.Client{Timeout: 10 * time.Second}

func nodeGet(path string, out any) error {
	t0 := time.Now()
	resp, err := nodeClient.Get(nodeAPI + path)
	if err != nil {
		log.Printf("node: GET %s FAILED after %dms: %v", path, time.Since(t0).Milliseconds(), err)
		return err
	}
	defer resp.Body.Close()
	if dt := time.Since(t0); dt > 2*time.Second { // a slow node call is an early warning of the stall
		log.Printf("node: GET %s SLOW %dms (status %d)", path, dt.Milliseconds(), resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("node http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// refreshWork fetches a fresh template (paying to the pool) from the node, at
// most every ~2s, and computes the share target from the network target.
func refreshWork() (*work, error) {
	workMu.Lock()
	defer workMu.Unlock()
	if curWork != nil && time.Since(lastFetch) < 2*time.Second {
		return curWork, nil
	}
	var gw struct {
		ID, Header, Target, Seed string
		Height                   uint64
		Epoch                    uint64
	}
	if err := nodeGet("/getwork?addr="+poolAddr, &gw); err != nil {
		return nil, err
	}
	header, err1 := hex.DecodeString(gw.Header)
	seed, err2 := hex.DecodeString(gw.Seed)
	netT, ok := new(big.Int).SetString(gw.Target, 16)
	if err1 != nil || err2 != nil || !ok || len(header) != core.HeaderLen {
		return nil, errors.New("bad template from node")
	}
	shareT := new(big.Int).Lsh(netT, shareShift)
	if shareT.Cmp(core.MaxTarget) > 0 {
		shareT = new(big.Int).Set(core.MaxTarget)
	}
	// Rebuild on a new tip OR any header change (e.g. the node rebuilt its
	// template with a fresh Time after a restart). Serving a stale header would
	// make miners hash bytes the node no longer validates against, so every block
	// they find gets rejected as "insufficient proof of work" - wasting real work.
	if curWork == nil || curWork.nodeID != gw.ID || !bytes.Equal(curWork.header, header) {
		curWork = &work{nodeID: gw.ID, header: header, seed: seed, height: gw.Height,
			netTarget: netT, shareTarget: shareT, seen: map[uint64]bool{}}
	}
	lastFetch = time.Now()
	return curWork, nil
}

func hashFor(w *work, nonce uint64) (h [32]byte, ok bool) {
	hdr := make([]byte, len(w.header))
	copy(hdr, w.header)
	for i := 0; i < 8; i++ {
		hdr[core.NonceOffset+i] = byte(nonce >> (8 * i))
	}
	epoch := w.height / core.EpochLength
	vmMu.Lock()
	if curParams == nil || epoch != curParamEpoch {
		curParams = nm.DeriveParams(w.seed)
		curParamEpoch = epoch
	}
	p := curParams
	vmMu.Unlock()
	select {
	case vmSem <- struct{}{}:
	default:
		return h, false
	}
	defer func() { <-vmSem }()
	ev, _ := vmPool.Get().(*epochVM)
	if ev == nil || ev.epoch != epoch {
		ev = &epochVM{vm: nm.NewVM(p), epoch: epoch}
	}
	h = ev.vm.Hash(hdr, w.height)
	vmPool.Put(ev)
	return h, true
}

// ------------------------------------------------------------------- handlers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func getworkHandler(w http.ResponseWriter, r *http.Request) {
	if dbMode && !poolActive.Load() {
		writeJSON(w, 503, map[string]string{"error": "standby - this pool node is passive"})
		return
	}
	addr := r.URL.Query().Get("addr")
	if !core.ValidAddr(addr) {
		writeJSON(w, 400, map[string]string{"error": "bad or missing addr"})
		return
	}
	wk, err := refreshWork()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "pool backend unavailable"})
		return
	}
	id := wk.nodeID + "|" + addr
	if worker := sanitizeWorker(r.URL.Query().Get("worker")); worker != "" {
		id += "~" + worker // display-only rig label; rides the work id back to submitwork
	}
	writeJSON(w, 200, map[string]any{
		"id":         id,
		"header":     hex.EncodeToString(wk.header),
		"target":     core.TargetToHex(wk.shareTarget),
		"seed":       hex.EncodeToString(wk.seed),
		"height":     wk.height,
		"epoch":      wk.height / core.EpochLength,
		"extranonce": extranonceFor(addr),
	})
}

// ===== HEAVY INSTRUMENTATION (test build) =====
// Per-outcome atomic counters; an aggregate breakdown is logged every 2s (always on),
// and -debug-log adds a per-submit line (full volume → use -debug-file for a file sink).
var debugLog bool
var (
	cSubmits, cAccepted, cBlocksFound int64
	cBadJSON, cBadNonce, cBadID, cBadAddr,
	cRateLim, cNotBound, cStale, cDup, cBusy, cLowDiff int64
)

func sh(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// dbgSubmit logs one submit's outcome (only when -debug-log).
func dbgSubmit(reason, miner, worker string, nonce uint64) {
	if debugLog {
		log.Printf("SUBMIT result=%-10s addr=%s worker=%q nonce=%d", reason, sh(miner), worker, nonce)
	}
}

// activeGauge reports the live connection/active-miner health: how many distinct miners
// submitted an accepted share in the last 5m (= active_miners on the dashboard), how many
// share events are buffered in memory (this is what resets to 0 on restart and rebuilds —
// the source of the "pool looks dead after a reboot" false alarm), and the pool hashrate.
func activeGauge() (active, bufferedEvents int, hashrate float64) {
	const window = 5 * time.Minute
	cut := time.Now().Add(-window)
	hps := hashesPerShare()
	seen := map[string]struct{}{}
	var recentWeight float64
	shareMu.Lock()
	bufferedEvents = len(shareEv)
	for _, e := range shareEv {
		if e.t.After(cut) {
			seen[e.miner] = struct{}{}
			recentWeight += e.weight
		}
	}
	shareMu.Unlock()
	active = len(seen)
	if hps > 0 {
		hashrate = recentWeight * hps / window.Seconds()
	}
	return
}

// statsLoop logs ONE concise health line per minute (rates over the last minute). Full per-reason
// counters stay available via /api/poolstats; per-submit detail is behind -debug-log. No log spam.
func statsLoop() {
	var pAcc, pLd, pSub, pNb int64
	for {
		time.Sleep(60 * time.Second)
		sub := atomic.LoadInt64(&cSubmits)
		acc := atomic.LoadInt64(&cAccepted)
		ld := atomic.LoadInt64(&cLowDiff)
		nb := atomic.LoadInt64(&cNotBound)
		active, _, hr := activeGauge()
		role := "single"
		if dbMode {
			if poolActive.Load() {
				role = "active"
			} else {
				role = "standby"
			}
		}
		log.Printf("health: active=%d hashrate=%.0fH/s accepted=%.1f/s lowdiff=%.1f/s notbound=%.1f/s submits=%.1f/s blocks=%d role=%s",
			active, hr, float64(acc-pAcc)/60, float64(ld-pLd)/60, float64(nb-pNb)/60, float64(sub-pSub)/60,
			atomic.LoadInt64(&cBlocksFound), role)
		pAcc, pLd, pSub, pNb = acc, ld, sub, nb
	}
}

// ===== HA role (active/standby), derived from this node's Postgres role =====
var poolActive atomic.Bool

// roleLoop tracks whether this node's Postgres is primary (writable). Primary → pool ACTIVE
// (accepts shares + pays); replica → pool STANDBY (passive). Promoting the replica's Postgres
// (Patroni / manual) therefore auto-activates that node's pool — no separate pool promote.
func roleLoop() {
	var errN int
	for {
		if rec, err := dbInRecovery(); err == nil {
			errN = 0
			active := !rec
			if poolActive.Swap(active) != active {
				if active {
					log.Printf("HA: node ACTIVE (Postgres primary) — accepting shares + payouts")
					// We may have been a long-running standby whose in-memory maps are stale (the
					// former leader kept assigning extranonces + sliding the PPLNS window while we
					// sat passive). Re-read the latest Postgres snapshots NOW so promoted-pool
					// miners are accepted immediately (no post-failover notbound transient) and the
					// first block credits the live window. Both loads REPLACE (never append).
					loadExtranonce()
					loadPPLNSSnapshot()
				} else {
					log.Printf("HA: node STANDBY (Postgres in recovery) — passive")
				}
			}
		} else {
			errN++
			if errN%10 == 1 { // ~every 30s while the DB role probe is failing (don't flood)
				log.Printf("HA: ⚠ role probe (pg_is_in_recovery) ERROR: %v — role held at active=%v", err, poolActive.Load())
			}
		}
		time.Sleep(3 * time.Second)
	}
}

// healthHandler is the load-balancer probe: 200 only when this node is the ACTIVE pool
// (or single-node), so the LB routes only to the live writer; a standby returns 503.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	role, healthy := "single", true
	if dbMode {
		if poolActive.Load() {
			role = "active"
		} else {
			role, healthy = "standby", false
		}
	}
	code := 200
	if !healthy {
		code = 503
	}
	writeJSON(w, code, map[string]any{"ok": healthy, "role": role, "instance": instanceID, "height": st.ChainHeight})
}

func submitworkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "POST only"})
		return
	}
	atomic.AddInt64(&cSubmits, 1)
	if dbMode && !poolActive.Load() {
		writeJSON(w, 503, map[string]string{"error": "standby - this pool node is passive"})
		return
	}
	var req struct {
		ID    string          `json:"id"`
		Nonce json.RawMessage `json:"nonce"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		atomic.AddInt64(&cBadJSON, 1)
		dbgSubmit("badjson", "", "", 0)
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	// Accept the nonce as a JSON number OR a quoted string: a 64-bit nonce
	// (extranonce in the top bits) exceeds JS's 2^53 safe-integer range, so the
	// browser miner must send it as a string; the native miner sends a number.
	nonce, perr := strconv.ParseUint(strings.Trim(string(req.Nonce), "\""), 10, 64)
	if perr != nil {
		atomic.AddInt64(&cBadNonce, 1)
		dbgSubmit("badnonce", "", "", 0)
		writeJSON(w, 400, map[string]string{"error": "bad nonce"})
		return
	}
	raw := req.ID
	worker := ""
	if j := strings.IndexByte(raw, '~'); j >= 0 { // optional "~<worker>" display label
		worker = sanitizeWorker(raw[j+1:])
		raw = raw[:j]
	}
	i := strings.LastIndex(raw, "|")
	if i < 0 {
		atomic.AddInt64(&cBadID, 1)
		dbgSubmit("badworkid", "", worker, nonce)
		writeJSON(w, 400, map[string]string{"error": "bad work id"})
		return
	}
	nodeID, miner := raw[:i], raw[i+1:]
	if !core.ValidAddr(miner) {
		atomic.AddInt64(&cBadAddr, 1)
		dbgSubmit("badaddr", miner, worker, nonce)
		writeJSON(w, 400, map[string]string{"error": "bad miner addr"})
		return
	}
	// Anti-flood: bound the expensive hashFor() per address + globally (NOT per IP/connection —
	// farm proxies multiplex thousands of workers). Cheap reject before any hashing.
	if !submitAllowed(miner, time.Now()) {
		atomic.AddInt64(&cRateLim, 1)
		dbgSubmit("ratelimited", miner, worker, nonce)
		writeJSON(w, 429, map[string]string{"error": "rate limited - slow down"})
		return
	}
	// Share-binding: the nonce's top-16-bit tag must equal the extranonce this
	// address was issued. This makes a solution valid for exactly one miner, so
	// nobody can claim another miner's share by submitting it under their address.
	if (nonce>>48)&0xFFFF != extranonceFor(miner) {
		atomic.AddInt64(&cNotBound, 1)
		dbgSubmit("notbound", miner, worker, nonce)
		writeJSON(w, 400, map[string]string{"error": "nonce not bound to your extranonce - update your miner"})
		return
	}
	workMu.Lock()
	wk := curWork
	stale := wk == nil || wk.nodeID != nodeID
	workMu.Unlock()
	if stale {
		atomic.AddInt64(&cStale, 1)
		dbgSubmit("stale", miner, worker, nonce)
		writeJSON(w, 200, map[string]string{"result": "stale"})
		return
	}
	// dedup
	workMu.Lock()
	if wk.seen[nonce] {
		workMu.Unlock()
		atomic.AddInt64(&cDup, 1)
		dbgSubmit("duplicate", miner, worker, nonce)
		writeJSON(w, 200, map[string]string{"result": "duplicate"})
		return
	}
	wk.seen[nonce] = true
	workMu.Unlock()

	h, hok := hashFor(wk, nonce)
	if !hok {
		atomic.AddInt64(&cBusy, 1)
		dbgSubmit("busy503", miner, worker, nonce)
		writeJSON(w, 503, map[string]string{"error": "validator busy, retry"})
		return
	}
	if !core.HashMeetsTarget(h, wk.shareTarget) {
		atomic.AddInt64(&cLowDiff, 1)
		dbgSubmit("lowdiff", miner, worker, nonce)
		writeJSON(w, 400, map[string]string{"error": "low difficulty share"})
		return
	}
	// valid share
	atomic.AddInt64(&cAccepted, 1)
	dbgSubmit("ACCEPTED", miner, worker, nonce)
	// HOT PATH: the PPLNS window is kept IN MEMORY in BOTH modes — there is NO per-share DB
	// write (a row-per-share Postgres insert is the classic pool anti-pattern that collapses
	// throughput under real load). In db-mode the window is snapshotted to ONE Postgres row
	// periodically (pplnsSnapshotLoop) and on each block; the durable money ledger is `earned`,
	// credited per block.
	st.mu.Lock()
	st.Shares[miner]++ // display round counter
	st.RoundShares++
	addPPLNS(miner, 1) // PPLNS window = the actual payout basis
	st.mu.Unlock()
	recordShare(miner, worker)

	block := core.HashMeetsTarget(h, wk.netTarget)
	if block {
		// forward the real block to the node
		body, _ := json.Marshal(map[string]any{"id": nodeID, "nonce": nonce})
		resp, err := http.Post(nodeAPI+"/submitwork", "application/json", strings.NewReader(string(body)))
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var rr struct{ Result, Error string }
			_ = json.Unmarshal(raw, &rr)
			if rr.Result == "accepted" {
				onBlockFound(wk.height)
				log.Printf("pool: BLOCK %d found via miner %s", wk.height, miner[:12])
			} else {
				log.Printf("pool: block forward not accepted: %s", rr.Error)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"result": "share", "block": block})
}

// pplnsN is the PPLNS window size in share-weight = pplnsMult × shares-per-block (set in main
// from -shareshift and -pplns-n). Pool share difficulty = netDiff/2^shareshift, so a block
// takes ~2^shareshift shares regardless of network difficulty → the window is stable in "blocks".
var pplnsN float64 = 8192 // safe default (2 blocks at shareshift 12); overwritten in main

// addPPLNS appends a share's weight to the sliding PPLNS window and trims the oldest beyond
// pplnsN. Caller MUST hold st.mu.
func addPPLNS(miner string, w float64) {
	if w <= 0 {
		return
	}
	st.PPLNS = append(st.PPLNS, pplnsEntry{miner, w})
	st.PplnsSum += w
	for st.PplnsSum > pplnsN && len(st.PPLNS) > 1 {
		st.PplnsSum -= st.PPLNS[0].W
		st.PPLNS = st.PPLNS[1:]
	}
}

// savePPLNSSnapshot marshals the in-memory PPLNS window and upserts it into the single Postgres
// snapshot row (db-mode only). This replaces the per-share INSERT: one write every ~20s + on each
// block instead of ~100/s. Safe to call from any goroutine (takes st.mu only to read the window).
func savePPLNSSnapshot() {
	if !dbMode {
		return
	}
	st.mu.Lock()
	raw, err := json.Marshal(st.PPLNS)
	sum := st.PplnsSum
	st.mu.Unlock()
	if err != nil {
		log.Printf("pplns snapshot: marshal: %v", err)
		return
	}
	if err := dbSavePPLNSSnapshot(string(raw), sum); err != nil {
		log.Printf("pplns snapshot: save: %v", err)
	}
}

// loadPPLNSSnapshot restores the PPLNS window from the Postgres snapshot at startup/failover
// (db-mode only), so a restarted or promoted node resumes on the same recent-shares payout basis
// instead of from an empty window. The snapshot replicates via Patroni to the standby.
func loadPPLNSSnapshot() {
	if !dbMode {
		return
	}
	raw, _, err := dbLoadPPLNSSnapshot()
	if err != nil {
		log.Printf("pplns snapshot: load: %v", err)
		return
	}
	if raw == "" {
		return
	}
	var win []pplnsEntry
	if err := json.Unmarshal([]byte(raw), &win); err != nil {
		log.Printf("pplns snapshot: unmarshal: %v", err)
		return
	}
	st.mu.Lock()
	st.PPLNS = win
	st.PplnsSum = 0
	for _, e := range win { // recompute the sum from the window (defensive against a drifted cache)
		st.PplnsSum += e.W
	}
	n := len(st.PPLNS)
	sum := st.PplnsSum
	st.mu.Unlock()
	log.Printf("pplns: restored %d-entry window from Postgres snapshot (sum=%.0f) — payout basis survives restart/failover", n, sum)
}

// ---- submit anti-flood -----------------------------------------------------------------
// Protects the expensive hashFor(). Keyed NOT by IP or connection (a farm proxy multiplexes
// thousands of workers over one IP/connection — limiting those would break legit farms) but by
// (a) a generous PER-ADDRESS token bucket so one address can't hog CPU, and (b) a GLOBAL cap
// bounding total hashFor()/sec regardless of source. Vardiff-regulated legit load stays far
// below both; only a flood is throttled, and cheaply (before any hashing).
type tbucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (b *tbucket) allow(rate, burst float64, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * rate
		if b.tokens > burst {
			b.tokens = burst
		}
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Per-address submit rate = cheap front-line shed (flag-configurable, raised from 200→1000/s).
// GENEROUS: legit miners/farms are vardiff-regulated to ~1 share/interval per connection, so even
// a single wallet with thousands of connections stays far below. (Note: actual validation
// throughput is bounded by the serialized single-VM hashFor ~260/s — raising this further only
// matters once validation is parallelized / moved to the stratum.)
var (
	perAddrRate  = 1000.0 // submits/sec sustained per miner address
	perAddrBurst = 2000.0
	addrMu       sync.Mutex
	addrBuckets  = map[string]*tbucket{}
	addrSeen     = map[string]time.Time{}
)

// submitAllowed is the cheap per-address shed (before dedup/hashFor). Returns false to reject.
func submitAllowed(miner string, now time.Time) bool {
	addrMu.Lock()
	b := addrBuckets[miner]
	if b == nil {
		b = &tbucket{}
		addrBuckets[miner] = b
	}
	addrSeen[miner] = now
	addrMu.Unlock()
	return b.allow(perAddrRate, perAddrBurst, now)
}

// pruneBuckets drops idle per-address buckets so the map can't grow unbounded from churn.
func pruneBuckets() {
	cut := time.Now().Add(-15 * time.Minute)
	addrMu.Lock()
	for a, t := range addrSeen {
		if t.Before(cut) {
			delete(addrSeen, a)
			delete(addrBuckets, a)
		}
	}
	addrMu.Unlock()
}

// onBlockFound credits this block's reward (minus fee) across the PPLNS window — the last N
// shares' weight, NOT the current round. The window is NOT cleared (it slides), so each share
// is paid for every block while it stays in the window; total emitted per block still equals
// the pot. This makes pool-hopping unprofitable. Round counters are reset for display only.
func onBlockFound(height uint64) {
	atomic.AddInt64(&cBlocksFound, 1)
	reward := core.BlockSubsidy(height)
	pot := reward - reward*feePermil/1000
	// Credit the block's pot across the IN-MEMORY PPLNS window — IDENTICAL computation in both
	// modes (computeBlockCredits). Only the persistence of `earned` differs: the durable Postgres
	// ledger in db-mode (one batched tx per block), pool.json otherwise.
	st.mu.Lock()
	credits := computeBlockCredits(st.PPLNS, pot, poolAddr)
	if !dbMode {
		for m, amt := range credits {
			st.Earned[m] += amt
		}
	}
	st.Shares = map[string]float64{} // display round counters only
	st.RoundShares = 0
	st.Found++
	st.mu.Unlock()
	if dbMode {
		if err := dbCreditBlock(credits); err != nil { // durable money ledger, one tx per block
			log.Printf("db: credit block: %v", err)
		}
		savePPLNSSnapshot() // persist the (slid) window right after a block — cheap and rare
		saveFound()         // persist the block count so it survives a restart/failover
	} else {
		st.save()
	}
}

// computeBlockCredits splits `pot` across the PPLNS window proportionally to each miner's weight.
// poolAddr is counted in the denominator (its own mining is already paid by the coinbase) but is
// never credited. Pure → unit-tested; identical in db and pool.json modes.
func computeBlockCredits(win []pplnsEntry, pot uint64, pool string) map[string]uint64 {
	var total float64
	perMiner := map[string]float64{}
	for _, e := range win {
		total += e.W
		if e.M == pool {
			continue
		}
		perMiner[e.M] += e.W
	}
	credits := map[string]uint64{}
	if total > 0 {
		for m, wsum := range perMiner {
			credits[m] = uint64(float64(pot) * (wsum / total))
		}
	}
	return credits
}

// ------------------------------------------------------------------ payouts

func payoutLoop() {
	reconcile() // align the books with the chain before paying anything
	cycle := 0
	for {
		time.Sleep(60 * time.Second)
		cycle++
		pruneBuckets() // drop idle per-address anti-flood buckets
		t0 := time.Now()
		reconcile() // refresh: confirmed payouts land in Delivered, dropped ones become payable again (also keeps a standby's caches warm)
		rdt := time.Since(t0).Milliseconds()
		if rdt > 5000 { // reconcile should be fast; a slow one is the early sign of a node/DB stall
			log.Printf("PAYOUT/cycle%d: reconcile SLOW %dms (node or DB lagging)", cycle, rdt)
		}
		if dbMode {
			// Single-writer election: only the lease holder sends payouts, so two live
			// instances can never both pay (anti-split-brain on the money path). Every
			// outcome is logged so a stalled lease is never silent again.
			ok, err := dbLeaderAcquire(instanceID, 90*time.Second)
			if err != nil {
				log.Printf("PAYOUT/cycle%d: ⚠ leader-acquire ERROR — NO PAYOUTS this cycle: %v", cycle, err)
				continue
			}
			if !ok {
				log.Printf("PAYOUT/cycle%d: not leader (lease held elsewhere) — skipping", cycle)
				continue
			}
		}
		// Only matured coinbase is spendable; pay out of that and pay PARTIAL if
		// owed exceeds it (the rest follows as more blocks mature).
		var bal struct {
			Spendable uint64 `json:"spendable"`
		}
		if err := nodeGet("/balance?addr="+poolAddr, &bal); err != nil {
			log.Printf("PAYOUT/cycle%d: ⚠ node balance ERROR — NO PAYOUTS this cycle: %v", cycle, err)
			continue
		}
		avail := bal.Spendable
		// Payable per miner = owed (chain-derived) MINUS anything already in flight,
		// so a payout is never sent twice while its tx is still confirming.
		st.mu.Lock()
		inflightPer := map[string]uint64{}
		for _, fl := range st.InFlight {
			inflightPer[fl.Miner] += fl.Gross
		}
		var list []due
		var totalPayable uint64
		for m, owed := range st.Owed {
			if m == poolAddr || inflightPer[m] >= owed {
				continue
			}
			if a := owed - inflightPer[m]; a >= minPayout {
				list = append(list, due{m, a})
				totalPayable += a
			}
		}
		h := st.ChainHeight
		nInflight := len(st.InFlight)
		st.mu.Unlock()
		// Split the spendable budget across miners PROPORTIONALLY to what each is
		// owed (a miner owed 1000 drains ~10x faster than one owed 100), instead of
		// fully paying whoever came first. Under scarcity the big debts drain in
		// step with the small ones rather than starving behind them.
		plan := planPayouts(list, avail, minPayout)
		log.Printf("PAYOUT/cycle%d: leader=ok reconcile=%dms spendable=%s payable=%s due=%d planned=%d inflight=%d",
			cycle, rdt, crb(avail), crb(totalPayable), len(list), len(plan), nInflight)
		if len(plan) == 0 && len(list) > 0 {
			log.Printf("PAYOUT/cycle%d: ⚠ %d miners owed %s but NOTHING planned (spendable=%s) — coinbase still maturing?",
				cycle, len(list), crb(totalPayable), crb(avail))
		}
		sent, deferred := 0, 0
		for _, d := range plan {
			txid, err := send(d.m, d.amt)
			if err != nil {
				deferred++
				log.Printf("PAYOUT/cycle%d: %s -> %s DEFERRED: %v", cycle, crb(d.amt), d.m[:12], err)
				continue
			}
			st.mu.Lock()
			st.InFlight[txid] = &inflight{Miner: d.m, Gross: d.amt, SentHeight: h}
			st.mu.Unlock()
			if dbMode {
				_ = dbInflightAdd(txid, d.m, d.amt, h)
				_ = dbPayoutLog(txid, d.m, d.amt, h)
			}
			st.save()
			sent++
			log.Printf("PAYOUT/cycle%d: sent %s -> %s (%s) [awaiting confirmation]", cycle, crb(d.amt), d.m[:12], txid[:12])
		}
		if len(plan) > 0 {
			log.Printf("PAYOUT/cycle%d: done sent=%d deferred=%d", cycle, sent, deferred)
		}
	}
}

// due is one miner's payable amount this cycle.
type due struct {
	m   string
	amt uint64
}

// planPayouts decides how much to pay each miner from `avail` this cycle. If the
// budget covers everyone, each gets their full owed. Under scarcity it splits the
// budget PROPORTIONALLY to each miner's debt (owed 1000 -> 10x the slice of owed
// 100), largest first, skipping slices below minPayout so we never burn a fee on
// dust. Pure function -> unit-tested.
func planPayouts(list []due, avail, minPayout uint64) []due {
	if len(list) == 0 || avail < minPayout {
		return nil
	}
	var total uint64
	for _, d := range list {
		total += d.amt
	}
	sort.Slice(list, func(i, j int) bool { return list[i].amt > list[j].amt })
	scarce := avail < total
	budget := avail
	var plan []due
	for _, d := range list {
		if budget < minPayout {
			break
		}
		pay := d.amt
		if scarce {
			// proportional slice of the (original) budget by share of total owed:
			// pay = avail * amt / total, exact integer math (no float rounding, no
			// uint64 overflow).
			pay = new(big.Int).Div(
				new(big.Int).Mul(new(big.Int).SetUint64(avail), new(big.Int).SetUint64(d.amt)),
				new(big.Int).SetUint64(total),
			).Uint64()
			if pay > d.amt {
				pay = d.amt
			}
		}
		if pay > budget {
			pay = budget
		}
		if pay < minPayout {
			continue // would be dust; this miner accrues for a later cycle
		}
		plan = append(plan, due{d.m, pay})
		budget -= pay
	}
	return plan
}

var sendMu sync.Mutex
var nextNonce uint64 // local nonce counter (covers pending mempool txs)

func send(to string, amount uint64) (string, error) {
	sendMu.Lock()
	defer sendMu.Unlock()
	var status struct {
		Height uint64 `json:"height"`
		Fee    uint64 `json:"fee_suggested"`
	}
	if err := nodeGet("/status", &status); err != nil {
		return "", err
	}
	var acc struct {
		Balance uint64 `json:"balance"`
		Nonce   uint64 `json:"nonce"`
	}
	if err := nodeGet("/balance?addr="+poolAddr, &acc); err != nil {
		return "", err
	}
	fee := status.Fee
	if fee == 0 {
		fee = 1000
	}
	if amount <= fee {
		return "", errors.New("amount below fee")
	}
	netAmt := amount - fee // miner covers the tx fee out of their payout
	if acc.Balance < netAmt+fee {
		return "", errors.New("pool wallet has no spendable balance yet")
	}
	nonce := acc.Nonce
	if nextNonce > nonce {
		nonce = nextNonce
	}
	tx := &core.Tx{To: to, Amount: netAmt, Fee: fee, Nonce: nonce}
	core.SignTxAt(tx, priv, status.Height+1)
	body, _ := json.Marshal(tx)
	resp, err := http.Post(nodeAPI+"/tx", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var rr struct{ TxID, Error string }
	_ = json.Unmarshal(raw, &rr)
	if rr.Error != "" {
		if strings.Contains(rr.Error, "nonce") {
			nextNonce = 0
		}
		return "", errors.New(rr.Error)
	}
	nextNonce = nonce + 1
	return rr.TxID, nil
}

func crb(v uint64) string { return strconv.FormatFloat(float64(v)/float64(core.CoinUnit), 'f', -1, 64) + " CRB" }

// --------------------------------------------------------------------- stats

// workersHandler returns the per-worker (rig) breakdown for ONE address: live
// hashrate, shares and idle time over the same window as the pool hashrate.
// Purely informational - payouts are per-address and unaffected by worker labels.
func workersHandler(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("addr")
	if !core.ValidAddr(addr) {
		writeJSON(w, 400, map[string]string{"error": "bad or missing addr"})
		return
	}
	const window = 5 * time.Minute
	hps := hashesPerShare()
	cut := time.Now().Add(-window)
	type agg struct {
		n    float64
		last time.Time
	}
	m := map[string]*agg{}
	shareMu.Lock()
	for _, e := range shareEv {
		if e.miner != addr || e.t.Before(cut) {
			continue
		}
		name := e.worker
		if name == "" {
			name = "(default)"
		}
		a := m[name]
		if a == nil {
			a = &agg{}
			m[name] = a
		}
		a.n += e.weight
		if e.t.After(a.last) {
			a.last = e.t
		}
	}
	shareMu.Unlock()
	now := time.Now()
	workers := []map[string]any{}
	for name, a := range m {
		workers = append(workers, map[string]any{
			"worker":    name,
			"hashrate":  a.n * hps / window.Seconds(),
			"shares":    a.n,
			"idle_secs": int(now.Sub(a.last).Seconds()),
		})
	}
	writeJSON(w, 200, map[string]any{
		"address":     addr,
		"window_secs": int(window.Seconds()),
		"workers":     workers,
	})
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	const window = 5 * time.Minute
	hps := hashesPerShare()
	// shares per miner in the last `window`, for live hashrate
	cut := time.Now().Add(-window)
	recent := map[string]float64{}
	var recentTotal float64
	shareMu.Lock()
	for _, e := range shareEv {
		if e.t.After(cut) {
			recent[e.miner] += e.weight
			recentTotal += e.weight
		}
	}
	shareMu.Unlock()
	hashrateOf := func(n float64) float64 { return n * hps / window.Seconds() }

	st.mu.Lock()
	var totalOwed, totalPaid uint64
	seen := map[string]bool{}
	miners := []map[string]any{}
	add := func(m string) {
		if seen[m] || m == poolAddr {
			return
		}
		seen[m] = true
		miners = append(miners, map[string]any{
			"address":  m,
			"shares":   st.Shares[m],
			"owed":     st.Owed[m],
			"paid":     st.Paid[m],
			"earned":   st.Earned[m],
			"hashrate": hashrateOf(recent[m]),
		})
	}
	for m := range st.Shares {
		add(m)
	}
	for m := range st.Owed {
		totalOwed += st.Owed[m]
		add(m)
	}
	for _, p := range st.Paid {
		totalPaid += p
	}
	// Include every miner the pool has ever credited (even fully paid out, owed 0)
	// so the dashboard's "show all" can display the complete, auditable set.
	for m := range st.Paid {
		add(m)
	}
	for m := range st.Earned {
		add(m)
	}
	out := map[string]any{
		"pool_address":   poolAddr,
		"fee_permil":     feePermil,
		"blocks_found":   st.Found,
		"round_shares":   st.RoundShares,
		"min_payout":     minPayout,
		"active_miners":  len(recent),
		"pool_hashrate":  hashrateOf(recentTotal),
		"hashes_per_share": hps,
		"total_owed":     totalOwed,
		"total_paid":     totalPaid,
		"share_window_s": int(window.Seconds()),
		"miners":         miners,
	}
	st.mu.Unlock()
	writeJSON(w, 200, out)
}

// creditHandler lets the LOCAL faucet credit captcha shares to an address, so the
// captcha wallet earns a steady slice of pool blocks instead of only the rare
// full-block jackpot. The /pool/ path is reverse-proxied publicly, so a loopback
// check isn't enough (everything arrives from Apache as 127.0.0.1) - a shared
// secret is what stops an outsider crediting themselves shares. Disabled unless a
// secret is configured.
func creditHandler(w http.ResponseWriter, r *http.Request) {
	// /api/credit MINTS pool shares and is for the LOCAL faucet over loopback only. Reject
	// anything that arrived through the public reverse proxy (Caddy sets X-Forwarded-For /
	// X-Real-IP) — so the endpoint is unreachable from the internet even if the shared secret
	// ever leaks. The faucet calls 127.0.0.1:18754 directly and sets no such header.
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	if creditSecret == "" || r.Header.Get("X-Credit-Secret") != creditSecret {
		writeJSON(w, 403, map[string]string{"error": "forbidden"})
		return
	}
	addr := r.URL.Query().Get("addr")
	if !core.ValidAddr(addr) {
		writeJSON(w, 400, map[string]string{"error": "bad addr"})
		return
	}
	// HONEST, work-proportional credit: the caller reports the WORK actually done (expected
	// hashes for the solved captcha share, e.g. 800 via &work=800). We convert it to pool
	// shares at the SAME rate real miners earn them — shares = work / hashesPerShare() — so a
	// captcha solve gets strictly its proportional slice of the round and NEVER dilutes real
	// miners. hashesPerShare() follows the live network difficulty, so this stays fair over time.
	// Legacy &shares=N is still accepted (capped) for back-compat.
	var n float64
	if ws := r.URL.Query().Get("work"); ws != "" {
		work, _ := strconv.ParseFloat(ws, 64)
		hps := hashesPerShare()
		if work <= 0 || hps <= 0 {
			writeJSON(w, 503, map[string]string{"error": "pool not ready or bad work"})
			return
		}
		n = work / hps
	} else {
		n, _ = strconv.ParseFloat(r.URL.Query().Get("shares"), 64)
		if n <= 0 || n > 100 {
			n = 1
		}
	}
	st.mu.Lock()
	st.Shares[addr] += n
	st.RoundShares += n
	addPPLNS(addr, n) // captcha's fair, work-proportional weight into the in-memory PPLNS window
	st.mu.Unlock()
	recordShareW(addr, "captcha", n)
	writeJSON(w, 200, map[string]any{"result": "credited", "addr": addr, "shares": n})
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18754", "listen address")
	flag.StringVar(&nodeAPI, "node", "http://127.0.0.1:18751/api", "node API base")
	keyfile := flag.String("keyfile", "/opt/cerebra/faucet-wallet.txt", "pool wallet file with PRIVATE KEY line")
	fee := flag.Float64("fee", 1.0, "pool fee percent")
	shift := flag.Uint("shareshift", 8, "share target = netTarget << shift (bigger = easier shares)")
	minp := flag.Float64("minpayout", 0.05, "minimum CRB before a payout is sent")
	pplnsMult := flag.Float64("pplns-n", 2.0, "PPLNS window size in blocks (× shares-per-block); larger = smoother & more hop-resistant")
	// Generous per-address defaults: a single farm/proxy can front ~5000 workers under ONE
	// address; vardiff keeps each worker to ~1 share/interval, so ~thousands/s/address is legit.
	// The real global CPU guard is vmSem (parallel validation cap), NOT a fixed global token rate.
	subRate := flag.Float64("submit-rate", 20000, "per-address sustained submits/sec before throttling")
	subBurst := flag.Float64("submit-burst", 40000, "per-address submit burst")
	valConc := flag.Int("validate-concurrency", 0, "max parallel share validations (0 = auto: NumCPU-2)")
	dbgL := flag.Bool("debug-log", false, "log every submit's outcome (heavy)")
	dbgFile := flag.String("debug-file", "", "if set, also write all logs to this file (avoids journald rate-limit)")
	flag.StringVar(&statePath, "state", "/var/lib/cerebra/pool.json", "state file")
	flag.StringVar(&chainFile, "chain", "/var/lib/cerebra/blocks.jsonl", "node chain file for payout reconciliation")
	creditSecretFile := flag.String("credit-secret-file", "", "file with the shared secret guarding /api/credit (faucet captcha)")
	dbDSN := flag.String("db", "", "Postgres DSN for shared HA state (empty = local pool.json)")
	redisAddr := flag.String("redis", "", "Redis addr for hot counters (optional, HA mode)")
	flag.Parse()

	debugLog = *dbgL
	if *dbgFile != "" {
		if f, err := os.OpenFile(*dbgFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
			log.Printf("debug: logging to %s", *dbgFile)
		} else {
			log.Printf("debug: cannot open %s: %v", *dbgFile, err)
		}
	}

	if *creditSecretFile != "" {
		if b, err := os.ReadFile(*creditSecretFile); err == nil {
			creditSecret = strings.TrimSpace(string(b))
		}
	}

	feePermil = uint64(*fee * 10)
	shareShift = *shift
	minPayout = uint64(*minp * float64(core.CoinUnit))
	// PPLNS window (weight) = N blocks × shares-per-block. shares-per-block = 2^shareshift
	// (pool share difficulty = netDiff / 2^shareshift), so this is stable across difficulty.
	pplnsN = *pplnsMult * float64(uint64(1)<<shareShift)
	perAddrRate = *subRate
	perAddrBurst = *subBurst
	vmN := *valConc
	if vmN <= 0 {
		vmN = runtime.NumCPU() - 2
	}
	if vmN < 1 {
		vmN = 1
	}
	vmSem = make(chan struct{}, vmN)

	raw, err := os.ReadFile(*keyfile)
	if err != nil {
		log.Fatalf("read keyfile: %v", err)
	}
	var skHex string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "PRIVATE KEY") {
			f := strings.Fields(line)
			skHex = f[len(f)-1]
		}
	}
	sk, err := hex.DecodeString(strings.TrimSpace(skHex))
	if err != nil || len(sk) != ed25519.PrivateKeySize {
		log.Fatalf("bad private key in keyfile")
	}
	priv = ed25519.PrivateKey(sk)
	poolAddr = core.AddrFromPub(priv.Public().(ed25519.PublicKey))
	if *dbDSN != "" {
		if err := dbInit(*dbDSN, *redisAddr); err != nil {
			log.Fatalf("db init: %v", err)
		}
		hn, _ := os.Hostname()
		instanceID = fmt.Sprintf("%s-%d", hn, os.Getpid())
		if rec, err := dbInRecovery(); err == nil {
			poolActive.Store(!rec) // primary → active immediately (no 3s cold window)
		}
		go roleLoop()
		log.Printf("HA: db-mode ON (Postgres shared state), instance=%s, active=%v", instanceID, poolActive.Load())
	}
	st.load()
	loadAllSnapshots() // restore stats + PPLNS window + extranonce + block count (Postgres-first, then disk)
	reconcile() // align books with the chain at startup (recovers anything dropped on a restart)
	installShutdownFlush() // SIGTERM/SIGINT -> flush all state before exit (a planned restart loses nothing)
	log.Printf("pool: addr %s fee %.1f%% shareshift %d pplns-window %.0f submit-rate %.0f/s burst %.0f validate-conc %d minpayout %s listen %s",
		poolAddr, *fee, shareShift, pplnsN, perAddrRate, perAddrBurst, cap(vmSem), crb(minPayout), *listen)

	go statsLoop()
	go payoutLoop()
	go snapshotLoop() // single timer: flush stats/PPLNS/extranonce snapshots (disk + Postgres)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/getwork", getworkHandler)
	mux.HandleFunc("/api/submitwork", submitworkHandler)
	mux.HandleFunc("/api/poolstats", statsHandler)
	mux.HandleFunc("/api/workers", workersHandler)
	mux.HandleFunc("/api/credit", creditHandler)
	mux.HandleFunc("/api/health", healthHandler)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
