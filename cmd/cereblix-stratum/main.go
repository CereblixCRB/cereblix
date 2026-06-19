// cereblix-stratum: a Stratum <-> getwork bridge for the Cereblix pool.
//
// It speaks the Cryptonote/XMRig-style Stratum dialect (login / job / submit)
// on a TCP socket and translates each request to the pool's existing HTTP
// getwork/submitwork API. It does NOT hash or validate shares itself - the pool
// re-verifies every submitted share - so the battle-tested pool accounting is
// left completely untouched; this is a pure protocol adapter.
//
// Job/share convention (document this for any miner that targets us):
//   - blob   : the raw block header (HeaderLen bytes) as hex. The nonce is the
//     LAST 8 bytes (offset NonceOffset), little-endian. The TOP 16 bits
//     (blob bytes NonceOffset+6 and +7) are a RESERVED extranonce that
//     the pool pinned to your address - DO NOT change them. Vary only
//     the lower 48 bits (blob bytes NonceOffset .. NonceOffset+5).
//   - target : 32-byte big-endian hex. A share is valid iff
//     bigEndian(NeuroMorphHash(blob)) <= target.
//   - seed_hash : NeuroMorph epoch seed (hex) used to derive the VM params.
//   - submit.nonce : the 8 nonce bytes (offset NonceOffset) as hex, little-endian
//     byte order (same order they sit in the blob), i.e. 16 hex chars.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cereblix/core"
)

var (
	poolAPI   string
	pollEvery time.Duration
	// A pooled keep-alive client. The stdlib default keeps only MaxIdleConnsPerHost=2 warm
	// connections, so under load (>2 concurrent submits) every extra request opens a fresh TCP
	// connection and pays a handshake. That is invisible when the pool is local (~0.4ms) but
	// catastrophic when the ACTIVE pool is the .95 standby reached over the WireGuard hop (~23ms
	// RTT): the per-request handshake + latency caps submit throughput, shares back up, miners
	// resubmit, and you get a storm of submits with a low accept rate. Keeping a large warm pool
	// pipelines the ~23ms latency across many reused connections (~256/0.023s ≈ 11k submits/s
	// headroom vs the ~55/s real load), so a failover to .95 stays healthy.
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        512,
			MaxIdleConnsPerHost: 256,
			MaxConnsPerHost:     0, // don't cap concurrent submit fan-out
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // payloads are tiny JSON; gzip is pure overhead
		},
	}
	connSeq uint64 // global counter -> a unique 16-bit per-connection id (nonce bits 32-47)
	verbose bool   // -v: log every job sent and every share (with round-trip ms)
)

// vlog logs only when -v is set (per-job / per-share lines would flood a busy pool).
func vlog(format string, a ...any) {
	if verbose {
		log.Printf(format, a...)
	}
}

// --- diagnostics (-diag): aggregate counters logged every 5s, to PIN why a failover degrades
// (e.g. "active pool on .95 → 72% of submits stale/dup") instead of guessing. Per 5s window:
//
//	staleJobSubmit = the miner submitted for a job the bridge had ALREADY replaced (it lags the
//	                 job stream) — vs verdict staleDup, where the POOL rejected the share as stale.
//	verdict real/lowfb/staleDup/other = the pool's outcome mix (lowfb = the eased-vardiff feedback).
//	jobpush tip/tgt/hdr = WHY the bridge pushed a fresh job (new block / vardiff / template header).
//	                 A high `hdr` rate means the served template churns (the failover thrash suspect).
var diag bool
var (
	dSubmits, dStaleJob              atomic.Int64
	dReal, dLowfb, dStaleDup, dOther atomic.Int64
	dPushTip, dPushTgt, dPushHdr     atomic.Int64
)

func diagLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		log.Printf("DIAG 5s: submits=%d staleJobSubmit=%d | verdict real=%d lowfb=%d staleDup=%d other=%d | jobpush tip=%d tgt=%d hdr=%d",
			dSubmits.Swap(0), dStaleJob.Swap(0), dReal.Swap(0), dLowfb.Swap(0), dStaleDup.Swap(0), dOther.Swap(0),
			dPushTip.Swap(0), dPushTgt.Swap(0), dPushHdr.Swap(0))
	}
}

// ------------------------------------------------------------------- pool i/o

type poolWork struct {
	ID         string `json:"id"` // "<nodeID>|<addr>"
	Header     string `json:"header"`
	Target     string `json:"target"`
	Seed       string `json:"seed"`
	Height     uint64 `json:"height"`
	Extranonce uint64 `json:"extranonce"`
}

func fetchWork(addr, worker string) (*poolWork, error) {
	u := poolAPI + "/getwork?addr=" + addr
	if worker != "" {
		u += "&worker=" + worker // worker is pre-sanitized to URL-safe [A-Za-z0-9._-]
	}
	resp, err := httpClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pool getwork http %d", resp.StatusCode)
	}
	var w poolWork
	if err := json.NewDecoder(resp.Body).Decode(&w); err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body) // fully drain so the keep-alive connection is returned to the pool, not closed
	return &w, nil
}

// submitToPool forwards a solved nonce to the pool. Returns (accepted, message).
func submitToPool(poolID string, nonce uint64) (bool, string) {
	body, _ := json.Marshal(map[string]any{"id": poolID, "nonce": strconv.FormatUint(nonce, 10)})
	resp, err := httpClient.Post(poolAPI+"/submitwork", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return false, "pool unreachable"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		Result string `json:"result"`
		Error  string `json:"error"`
		Block  bool   `json:"block"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.Result == "share" {
		if r.Block {
			return true, "BLOCK"
		}
		return true, "OK"
	}
	if r.Error != "" {
		return false, r.Error
	}
	if r.Result != "" { // "stale" / "duplicate"
		return false, r.Result
	}
	return false, "rejected"
}

// jobFromWork builds the Stratum job object and returns it with the pool work id.
// It pins the address-bound extranonce into the top 16 bits of the blob's nonce.
func jobFromWork(w *poolWork, connID uint64) (job map[string]any, poolID, jobID string, err error) {
	hdr, e := hex.DecodeString(w.Header)
	if e != nil || len(hdr) != core.HeaderLen {
		return nil, "", "", fmt.Errorf("bad header from pool")
	}
	// Nonce layout (8 LE bytes at NonceOffset):
	//   bits  0-31 (bytes +0..+3) = the MINER's search space (XMRig varies these 4 bytes)
	//   bits 32-47 (bytes +4..+5) = a per-CONNECTION id we assign, so multiple rigs on the
	//                               SAME address never overlap. The pool ignores these bits.
	//   bits 48-63 (bytes +6..+7) = the address-bound extranonce the pool requires.
	// The miner must keep bytes +4..+7 untouched and vary only bytes +0..+3.
	for i := 0; i < 4; i++ {
		hdr[core.NonceOffset+i] = 0
	}
	hdr[core.NonceOffset+4] = byte(connID)
	hdr[core.NonceOffset+5] = byte(connID >> 8)
	hdr[core.NonceOffset+6] = byte(w.Extranonce)
	hdr[core.NonceOffset+7] = byte(w.Extranonce >> 8)

	jobID = w.ID
	if k := strings.Index(w.ID, "|"); k >= 0 {
		jobID = w.ID[:k] // node work hash; changes every new template; opaque to the miner
	}
	job = map[string]any{
		"blob":      hex.EncodeToString(hdr),
		"job_id":    jobID,
		"target":    w.Target,
		"height":    w.Height,
		"seed_hash": w.Seed,
		"algo":      "nm/1",
	}
	return job, w.ID, jobID, nil
}

// ------------------------------------------------------------------ connection

type client struct {
	conn    net.Conn
	enc     *json.Encoder
	writeMu sync.Mutex

	addr    string // CRB payout address
	worker  string // optional rig label (from "wallet.worker" or rigid), for the pool's per-worker stats
	session string
	connID  uint64 // unique per-connection id, pinned into nonce bits 32-47 (set once at accept)

	jobMu      sync.Mutex
	curJobID   string
	extranonce uint64            // address-bound tag the pool requires in the nonce's top 16 bits
	jobs       map[string]string // recent jobID -> full pool work id ("<nodeID>|<poolAddr>|<addr>")
	lastTgtHex string            // solo: target hex of the last pushed job (to detect a vardiff change)
	lastHeader string            // header hex of the last pushed job — detects a TEMPLATE change even
	//                              within the same tip (e.g. on a pool failover the new pool's node
	//                              serves a different Time/mempool template), so miners get fresh work
	//                              immediately instead of grinding the old template until the next block

	// solo share-difficulty / vardiff state (only used when -solo); guarded by sdMu.
	sdMu        sync.Mutex
	fixedDiff   *big.Int // non-nil: miner pinned its own diff (vardiff off)
	curDiff     *big.Int // current share difficulty
	emaInterval float64  // EMA of seconds between fresh shares
	lastShareAt time.Time
	shareN      int
}

func (c *client) setJob(jobID, poolID string, extranonce uint64, targetHex, header string) {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	if len(c.jobs) > 32 { // cap memory; recent jobs only
		c.jobs = map[string]string{}
	}
	c.jobs[jobID] = poolID
	c.curJobID = jobID
	c.extranonce = extranonce
	c.lastTgtHex = targetHex
	c.lastHeader = header
}

// curDiffStr returns the connection's current vardiff share difficulty as a short string.
func (c *client) curDiffStr() string {
	c.sdMu.Lock()
	defer c.sdMu.Unlock()
	if c.curDiff == nil {
		return "?"
	}
	return c.curDiff.String()
}

// short is the miner address truncated for log lines.
func (c *client) short() string {
	if len(c.addr) >= 12 {
		return c.addr[:12]
	}
	return c.addr
}

// writeTimeout bounds every write to a miner. Without it, a half-open TCP
// connection (a silent NAT/router/Wi-Fi drop) fills the kernel send buffer and
// Encode blocks FOREVER while holding writeMu -> the poller stops pushing new
// jobs and the submit-ack path freezes, so the miner grinds a stale job at full
// hashrate and never reconnects (the "full speed but stops solving shares" bug).
// On a write timeout we close the conn so the miner reconnects to fresh work.
const writeTimeout = 30 * time.Second

func (c *client) send(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.enc.Encode(v); err != nil {
		c.conn.Close() // unblocks the read loop + stops the poller -> miner reconnects
		return err
	}
	return nil
}

func (c *client) sendResult(id any, result any) {
	_ = c.send(map[string]any{"id": id, "jsonrpc": "2.0", "error": nil, "result": result})
}

func (c *client) sendError(id any, msg string) {
	_ = c.send(map[string]any{"id": id, "jsonrpc": "2.0", "result": nil,
		"error": map[string]any{"code": -1, "message": msg}})
}

func (c *client) pushJob(job map[string]any) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "method": "job", "params": job})
}

func sanitizeLogin(login string) string {
	// XMRig sends "wallet", "wallet.worker", or "wallet+difficulty".
	for _, sep := range []string{".", "+", ":", "/"} {
		if i := strings.Index(login, sep); i >= 0 {
			login = login[:i]
		}
	}
	return strings.TrimSpace(login)
}

// workerLabel pulls an optional rig/worker name from a "wallet.worker" login
// (XMRig and SRBMiner both send this form) or the rigid field, sanitized to a
// short URL- and work-id-safe label.
func workerLabel(login, rigid string) string {
	w := ""
	if i := strings.Index(login, "."); i >= 0 {
		w = login[i+1:]
		for _, sep := range []string{"+", ":", "/", "."} {
			if k := strings.Index(w, sep); k >= 0 {
				w = w[:k]
			}
		}
	}
	if w == "" {
		w = rigid
	}
	return sanitizeWorker(w)
}

func sanitizeWorker(s string) string {
	s = strings.TrimSpace(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			out = append(out, r)
			if len(out) >= 24 {
				break
			}
		}
	}
	return string(out)
}

func (c *client) handleLogin(id any, params json.RawMessage) bool {
	var p struct {
		Login, Pass, Agent string
		RigID              string `json:"rigid"`
	}
	_ = json.Unmarshal(params, &p)
	addr := sanitizeLogin(p.Login)
	if !core.ValidAddr(addr) {
		c.sendError(id, "invalid login: use your crb1... address as the username")
		return false
	}
	c.addr = addr
	c.worker = workerLabel(p.Login, p.RigID)
	// A miner may pin its own difficulty: "crb1...+50000" or password "diff=50000" (both modes).
	c.fixedDiff = parseRequestedDiff(p.Login, p.Pass)
	var sid [8]byte
	_, _ = rand.Read(sid[:])
	c.session = hex.EncodeToString(sid[:])

	w, err := fetchWork(addr, c.worker)
	if err != nil {
		c.sendError(id, "pool backend unavailable")
		return false
	}
	job, poolID, jobID, err := jobFromWork(w, c.connID)
	if err != nil {
		c.sendError(id, "bad template from pool")
		return false
	}
	// Per-miner vardiff in BOTH modes: serve an eased share target so every miner gets
	// steady accepted shares (keeps strict miners like SRBMiner connected). parseTarget(w.Target)
	// is the hardest creditable target (the pool's share target, or the network target in solo);
	// vardiff eases down from it. Real crediting / blocks are still decided by the backend.
	tgtHex := c.shareTargetHex(parseTarget(w.Target))
	job["target"] = tgtHex
	c.setJob(jobID, poolID, w.Extranonce, tgtHex, w.Header)
	c.sendResult(id, map[string]any{"id": c.session, "job": job, "status": "OK",
		"extensions": []string{"algo", "keepalive"}})
	mode := "pool"
	if soloMode {
		mode = "solo"
	}
	mode += " diff=" + c.curDiffStr()
	if c.fixedDiff != nil {
		mode += " (fixed)"
	}
	log.Printf("stratum: login %s… agent=%q [%s] -> job %s", addr[:12], p.Agent, mode, jobID)
	return true
}

func (c *client) handleSubmit(id any, params json.RawMessage) {
	var p struct{ ID, JobID, Nonce, Result string }
	_ = json.Unmarshal(params, &p)

	c.jobMu.Lock()
	poolID := c.jobs[p.JobID]
	if poolID == "" {
		poolID = c.jobs[c.curJobID]
	}
	ex := c.extranonce
	staleJob := p.JobID != "" && p.JobID != c.curJobID // miner is mining a job the bridge already replaced
	c.jobMu.Unlock()
	if poolID == "" {
		c.sendError(id, "no active job - re-login")
		return
	}
	if diag {
		dSubmits.Add(1)
		if staleJob {
			dStaleJob.Add(1)
		}
	}

	nonce, ok := parseNonceLE(p.Nonce)
	if !ok {
		c.sendError(id, "bad nonce")
		return
	}
	// XMRig varies only the low 32 bits and submits just those 4 bytes. Reconstruct the
	// full 64-bit nonce exactly as the miner hashed it: low 32 = submitted, bits 32-47 =
	// this connection's id (so two rigs on the same address don't collide), bits 48-63 =
	// the address-bound extranonce the pool requires.
	full := (nonce & 0xFFFFFFFF) | (c.connID << 32) | (ex << 48)
	t0 := time.Now()
	if soloMode {
		c.handleSubmitSolo(id, poolID, full, t0)
	} else {
		c.handleSubmitPool(id, poolID, full, t0)
	}
}

// handleSubmitPool is the pool-mode submit path. As in solo, the bridge serves a per-miner
// vardiff target (eased from the pool's real share target), forwards EVERY submit to the
// pool, and surfaces a sub-share-target nonce ("low difficulty share") as an ACCEPTED
// feedback share so strict miners (SRBMiner) stay connected. Crediting is unchanged: the
// pool still counts only nonces that meet its real share target, so pool accounting and
// payouts are untouched.
func (c *client) handleSubmitPool(id any, poolID string, full uint64, t0 time.Time) {
	accepted, msg := submitToPool(poolID, full)
	rtt := time.Since(t0).Milliseconds()
	if diag {
		switch {
		case accepted:
			dReal.Add(1)
		case strings.Contains(msg, "low difficulty") || strings.Contains(msg, "proof of work"):
			dLowfb.Add(1)
		case msg == "stale" || msg == "duplicate":
			dStaleDup.Add(1)
		default:
			dOther.Add(1)
		}
	}
	switch {
	case accepted && msg == "BLOCK":
		c.onSoloShare(time.Now())
		c.sendResult(id, map[string]any{"status": "OK"})
		log.Printf("stratum: ★ BLOCK found by %s…", c.short())
	case accepted: // real pool share (credited)
		c.onSoloShare(time.Now())
		c.sendResult(id, map[string]any{"status": "OK"})
		vlog("✓ share %s… diff=%s %dms", c.short(), c.curDiffStr(), rtt)
	case strings.Contains(msg, "low difficulty") || strings.Contains(msg, "proof of work"): // sub-target share -> keepalive feedback (pool: "low difficulty share"; node: "insufficient proof of work")
		c.onSoloShare(time.Now())
		c.sendResult(id, map[string]any{"status": "OK"})
		vlog("· feedback %s… diff=%s %dms", c.short(), c.curDiffStr(), rtt)
	case msg == "stale" || msg == "duplicate": // template rolled over / resend: soft-ack, next job follows
		c.sendResult(id, map[string]any{"status": "OK"})
		vlog("· %s %s… %dms", msg, c.short(), rtt)
	default:
		c.sendError(id, msg)
		vlog("✗ share %s… %s %dms", c.short(), msg, rtt)
	}
}

// handleSubmitSolo is the -solo submit path: the node decides what is a block, we
// surface sub-target nonces as accepted feedback shares and feed vardiff.
func (c *client) handleSubmitSolo(id any, poolID string, full uint64, t0 time.Time) {
	kind, msg := submitSolo(poolID, full)
	rtt := time.Since(t0).Milliseconds()
	switch kind {
	case "block":
		c.onSoloShare(time.Now())
		c.sendResult(id, map[string]any{"status": "OK"})
		log.Printf("stratum-solo: ★ BLOCK found by %s… %s (%dms)", c.short(), msg, rtt)
	case "feedback":
		c.onSoloShare(time.Now())
		c.sendResult(id, map[string]any{"status": "OK"})
		vlog("✓ share %s… diff=%s %dms", c.short(), c.curDiffStr(), rtt)
	case "stale":
		c.sendResult(id, map[string]any{"status": "OK"}) // soft-ack so the miner isn't spooked
		vlog("· stale %s… %dms", c.short(), rtt)
	default:
		vlog("✗ share %s… %s %dms", c.short(), msg, rtt)
		c.sendError(id, msg)
	}
}

// submitSolo forwards a solo nonce to the NODE and classifies the response. The
// node is the sole authority on what is a real block, so a found block can never
// be lost here: result "accepted" -> "block"; a sub-target nonce ("insufficient
// proof of work") -> a valid "feedback" share; a changed template -> "stale";
// anything else -> "reject".
func submitSolo(poolID string, nonce uint64) (kind, msg string) {
	body, _ := json.Marshal(map[string]any{"id": poolID, "nonce": strconv.FormatUint(nonce, 10)})
	resp, err := httpClient.Post(poolAPI+"/submitwork", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "reject", "node unreachable"
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r struct{ Result, Hash, Error string }
	_ = json.Unmarshal(raw, &r)
	if r.Result == "accepted" {
		return "block", r.Hash
	}
	e := strings.ToLower(r.Error)
	switch {
	case strings.Contains(e, "proof of work"), strings.Contains(e, "low difficulty"):
		return "feedback", ""
	case strings.Contains(e, "stale"), strings.Contains(e, "unknown work"):
		return "stale", ""
	case e == "":
		return "feedback", "" // not accepted, no error: treat as a feedback share
	default:
		return "reject", r.Error
	}
}

// parseNonceLE accepts the 8 nonce bytes as hex (little-endian byte order, as they
// sit in the blob) and returns the uint64 the pool expects.
func parseNonceLE(s string) (uint64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) == 0 || len(b) > 8 {
		// also tolerate a plain decimal uint64
		if n, e := strconv.ParseUint(strings.TrimSpace(s), 10, 64); e == nil {
			return n, true
		}
		return 0, false
	}
	var n uint64
	for i := 0; i < len(b); i++ { // little-endian
		n |= uint64(b[i]) << (8 * uint(i))
	}
	return n, true
}

// poller refreshes work and pushes a new job whenever the template changes.
func (c *client) poller(done <-chan struct{}) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if c.addr == "" {
				continue
			}
			w, err := fetchWork(c.addr, c.worker)
			if err != nil {
				continue
			}
			job, poolID, jobID, err := jobFromWork(w, c.connID)
			if err != nil {
				continue
			}
			tgtHex := c.shareTargetHex(parseTarget(w.Target)) // vardiff in both modes
			job["target"] = tgtHex
			c.jobMu.Lock()
			reason := -1 // why a fresh job is needed: 0=new tip/block, 1=vardiff target, 2=template header
			switch {
			case jobID != c.curJobID:
				reason = 0
			case tgtHex != c.lastTgtHex:
				reason = 1
			case w.Header != c.lastHeader:
				reason = 2
			}
			c.jobMu.Unlock()
			c.setJob(jobID, poolID, w.Extranonce, tgtHex, w.Header)
			if reason >= 0 {
				c.pushJob(job)
				if diag {
					switch reason {
					case 0:
						dPushTip.Add(1)
					case 1:
						dPushTgt.Add(1)
					case 2:
						dPushHdr.Add(1)
					}
				}
				vlog("→ job %s… id=%s diff=%s", c.short(), jobID, c.curDiffStr())
			}
		}
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()
	c := &client{conn: conn, enc: json.NewEncoder(conn), jobs: map[string]string{},
		connID: atomic.AddUint64(&connSeq, 1) & 0xFFFF}
	done := make(chan struct{})
	defer close(done)
	go c.poller(done)

	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if e := json.Unmarshal(line, &req); e != nil {
			continue
		}
		switch req.Method {
		case "login":
			c.handleLogin(req.ID, req.Params)
		case "submit":
			c.handleSubmit(req.ID, req.Params)
		case "keepalived":
			c.sendResult(req.ID, map[string]any{"status": "KEEPALIVED"})
		default:
			c.sendError(req.ID, "unknown method")
		}
	}
}

func main() {
	listen := flag.String("listen", ":3333", "stratum TCP listen address")
	flag.StringVar(&poolAPI, "pool", "http://127.0.0.1:18754/api", "pool HTTP API base")
	flag.DurationVar(&pollEvery, "poll", 2*time.Second, "how often to poll the pool for new work")
	flag.BoolVar(&soloMode, "solo", false, "solo mode: backend is a NODE; serve an easy vardiff share target for feedback (real blocks still go to the node)")
	flag.BoolVar(&verbose, "v", false, "verbose: log every job sent and every share with round-trip latency (ms)")
	flag.BoolVar(&diag, "diag", false, "diagnostics: every 5s log aggregate submit/job-push counters (staleJobSubmit, verdict mix, job-push reasons) — to pin why a failover degrades")
	noUpdate := flag.Bool("noupdate", false, "disable automatic self-update for this run (one-off; see -autoupdate to persist)")
	doUpdate := flag.Bool("update", false, "update to the latest released bridge (if newer) and exit")
	doVersion := flag.Bool("version", false, "print version and exit")
	autoUpd := flag.String("autoupdate", "", "persist auto-update preference: 'on' or 'off', then exit")
	flag.Parse()

	if *doVersion {
		printVersion()
		return
	}
	if *doUpdate {
		runUpdateOnce()
		return
	}
	if *autoUpd != "" {
		switch strings.ToLower(strings.TrimSpace(*autoUpd)) {
		case "on", "true", "1", "enable":
			setAutoUpdate(true)
		case "off", "false", "0", "disable":
			setAutoUpdate(false)
		default:
			log.Println("usage: cereblix-stratum -autoupdate on|off")
		}
		return
	}

	bootGuard(*listen) // confirm/rollback a freshly self-updated binary before we bind

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("stratum: listen %s: %v", *listen, err)
	}
	mode := "pool"
	if soloMode {
		mode = "solo (vardiff: default = pool diff, auto-tuned per miner; override with -p diff=N)"
	}
	log.Printf("cereblix-stratum v%s bridge on %s -> %s [%s]%s", stratumVersion, *listen, poolAPI, mode,
		map[bool]string{true: " verbose", false: ""}[verbose])
	go autoUpdateLoop(!*noUpdate) // authority-signed, verified, with rollback
	if diag {
		log.Printf("stratum: -diag ON — 5s aggregate submit/job-push counters")
		go diagLoop()
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("stratum: accept: %v", err)
			continue
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			tcp.SetKeepAlive(true)
			tcp.SetKeepAlivePeriod(30 * time.Second) // detect half-open peers at the TCP layer
		}
		go handleConn(conn)
	}
}
