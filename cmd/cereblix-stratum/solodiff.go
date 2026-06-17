package main

// Share-difficulty + variable difficulty (vardiff). Originally solo-only; now used in
// BOTH modes — the bridge serves each miner an eased per-connection share target so even
// weak miners (and strict ones like SRBMiner, which drop the connection if no share is
// accepted for ~60-90s) get steady accepted shares. In solo the backend is a NODE; in
// pool the backend is the pool, which still credits ONLY nonces meeting its real share
// target (a sub-target nonce is surfaced to the miner as a keepalive feedback share).
// The NODE/POOL remains the sole authority on real blocks / credited shares.
//
// Why it exists: in solo the node hands out work at the FULL network target.
// A normal CPU almost never meets it, so XMRig shows zero shares and looks
// dead. Here the bridge instead hands the miner an EASIER "share" target so it
// submits steadily (live hashrate + "accepted" feedback), exactly like a pool.
//
// Safety: the NODE remains the sole authority on what is a real block. Every
// submit is still forwarded to the node; the node accepts a real block and
// rejects a sub-target nonce ("insufficient proof of work"), which we surface to
// the miner as an accepted feedback share. The bridge never decides block
// validity itself, so a bug here can NEVER cause a found block to be lost.
//
// Difficulty model (matches the coin's own notion, core.WorkOf = 2^256/(t+1)):
//   - default share difficulty = network difficulty >> sharediffShift (12),
//     i.e. identical to what the pool serves (pool runs -shareshift 12).
//   - bounds: easiest = diff at core.MaxTarget (~4096); hardest = network diff
//     (at which a "share" IS a block). Both scale with network difficulty.
//   - vardiff auto-tunes each miner toward ~one share / vardiffTargetSecs based
//     on its observed rate, unless the miner pinned a fixed diff (see below).
//   - a miner may pick its own: login "crb1...+50000" or password "diff=50000"
//     (or a bare numeric password). That disables vardiff for that connection.

import (
	"math/big"
	"strconv"
	"strings"
	"time"

	"cereblix/core"
)

// soloMode is set by the -solo flag. It selects the BACKEND (node vs pool) and how a
// submit is classified; per-miner vardiff (below) now runs in BOTH modes.
var soloMode bool

const (
	sharediffShift    = 12   // default share diff = netDiff >> 12 (same as the pool)
	vardiffTargetSecs = 12.0 // aim for ~one share every this many seconds
)

var pow256 = new(big.Int).Lsh(big.NewInt(1), 256)

// targetToDiff converts a 256-bit target to difficulty (2^256/(target+1)).
func targetToDiff(t *big.Int) *big.Int {
	if t == nil || t.Sign() <= 0 {
		return big.NewInt(1)
	}
	return core.WorkOf(t)
}

// diffToTarget is the inverse: target = 2^256/diff - 1, clamped to a real,
// not-easier-than-MaxTarget value.
func diffToTarget(d *big.Int) *big.Int {
	if d == nil || d.Sign() <= 0 {
		return new(big.Int).Set(core.MaxTarget)
	}
	t := new(big.Int).Div(pow256, d)
	t.Sub(t, big.NewInt(1))
	if t.Sign() < 1 {
		t = big.NewInt(1)
	}
	if t.Cmp(core.MaxTarget) > 0 {
		t = new(big.Int).Set(core.MaxTarget)
	}
	return t
}

func clampDiff(d, lo, hi *big.Int) *big.Int {
	if d.Cmp(lo) < 0 {
		return new(big.Int).Set(lo)
	}
	if d.Cmp(hi) > 0 {
		return new(big.Int).Set(hi)
	}
	return new(big.Int).Set(d)
}

// parseTarget reads a hex network target; on garbage it falls back to MaxTarget
// (easiest) so we never accidentally hand out an impossibly hard target.
func parseTarget(hexs string) *big.Int {
	t, ok := new(big.Int).SetString(strings.TrimSpace(hexs), 16)
	if !ok || t.Sign() <= 0 {
		return new(big.Int).Set(core.MaxTarget)
	}
	return t
}

// diffBounds returns the easiest, hardest and default share difficulty for the
// current network target. Everything scales with network difficulty.
func diffBounds(netTarget *big.Int) (lo, hi, def *big.Int) {
	hi = targetToDiff(netTarget)                        // hardest: a share == a block
	lo = targetToDiff(new(big.Int).Set(core.MaxTarget)) // easiest the chain allows
	dt := new(big.Int).Lsh(netTarget, sharediffShift)   // pool-equivalent default target
	if dt.Cmp(core.MaxTarget) > 0 {
		dt = new(big.Int).Set(core.MaxTarget)
	}
	def = targetToDiff(dt)
	if def.Cmp(lo) < 0 {
		def = new(big.Int).Set(lo)
	}
	if def.Cmp(hi) > 0 {
		def = new(big.Int).Set(hi)
	}
	return
}

// parseRequestedDiff extracts a miner-chosen fixed difficulty from the raw login
// ("wallet+NNN") or the password ("diff=NNN" or a bare number). Returns nil when
// the miner did not ask for one (then vardiff drives the difficulty).
func parseRequestedDiff(rawLogin, pass string) *big.Int {
	if i := strings.IndexByte(rawLogin, '+'); i >= 0 {
		if d := parseDiffNum(rawLogin[i+1:]); d != nil {
			return d
		}
	}
	p := strings.TrimSpace(pass)
	if len(p) > 5 && strings.EqualFold(p[:5], "diff=") {
		if d := parseDiffNum(p[5:]); d != nil {
			return d
		}
	}
	return parseDiffNum(p) // a bare numeric password is taken as the difficulty
}

func parseDiffNum(s string) *big.Int {
	s = strings.TrimSpace(s)
	for _, sep := range []string{".", "/", ":", " ", ","} {
		if j := strings.Index(s, sep); j >= 0 {
			s = s[:j]
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n == 0 {
		return nil
	}
	return new(big.Int).SetUint64(n)
}

// shareTargetHex picks this connection's current share target for a fresh job:
// the pinned fixed diff, or the vardiff-managed curDiff, clamped to the bounds of
// the current network target. Never returns a target harder than the network's.
func (c *client) shareTargetHex(netTarget *big.Int) string {
	lo, hi, _ := diffBounds(netTarget)
	c.sdMu.Lock()
	switch {
	case c.fixedDiff != nil:
		c.curDiff = clampDiff(c.fixedDiff, lo, hi)
	case c.curDiff == nil:
		c.curDiff = lo // start at the EASIEST diff so the first share lands within seconds; vardiff then ramps up. Fixes SRBMiner's no-accepted-share watchdog and gives weak miners a fast first share.
	default:
		c.curDiff = clampDiff(c.curDiff, lo, hi)
	}
	tgt := diffToTarget(c.curDiff)
	c.sdMu.Unlock()
	if tgt.Cmp(netTarget) < 0 { // safety: a share is never harder than a block
		tgt = new(big.Int).Set(netTarget)
	}
	return core.TargetToHex(tgt)
}

// onSoloShare feeds the vardiff controller one fresh (non-stale) share. It nudges
// curDiff toward ~one share / vardiffTargetSecs using an EMA of the inter-share
// interval, with a gentle per-step clamp and a dead-band to avoid thrashing. The
// new diff is applied by the poller on the next job push. No-op for pinned diffs.
func (c *client) onSoloShare(now time.Time) {
	c.sdMu.Lock()
	defer c.sdMu.Unlock()
	if c.fixedDiff != nil {
		return
	}
	if !c.lastShareAt.IsZero() {
		dt := now.Sub(c.lastShareAt).Seconds()
		if dt < 0.001 {
			dt = 0.001
		}
		if c.emaInterval == 0 {
			c.emaInterval = dt
		} else {
			c.emaInterval = 0.7*c.emaInterval + 0.3*dt
		}
	}
	c.lastShareAt = now
	c.shareN++
	if c.shareN < 4 || c.emaInterval == 0 || c.curDiff == nil {
		return // warm up before reacting
	}
	ratio := c.emaInterval / vardiffTargetSecs
	if ratio > 0.7 && ratio < 1.3 {
		return // close enough; leave it alone
	}
	if ratio > 2 {
		ratio = 2
	} else if ratio < 0.5 {
		ratio = 0.5
	}
	// shares too fast (interval < target -> ratio < 1) => raise diff: newDiff = curDiff/ratio.
	nd := new(big.Float).Quo(new(big.Float).SetInt(c.curDiff), big.NewFloat(ratio))
	if ndi, _ := nd.Int(nil); ndi.Sign() > 0 {
		c.curDiff = ndi
	}
}
