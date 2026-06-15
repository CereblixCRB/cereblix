package core

import (
	"strconv"
	"strings"
)

// Network-upgrade machinery (BIP9-style readiness gating).
//
// A consensus rule change (e.g. the fee market) is given an activation FLOOR
// height (e.g. FeeMarketHeight) but does NOT flip there unconditionally. Instead
// every block advertises, in its coinbase, the consensus version its miner's
// node runs. The new rule activates at the first height >= the floor whose
// preceding SignalWindow blocks reached SignalThreshold% of the new version.
//
// This makes a fork split-proof: it cannot activate until a supermajority of
// hashrate is already on the new software, so the minority left behind has
// negligible work and never becomes the heavier chain. The signal lives in the
// coinbase Sig field, which is free-form and unvalidated, so tagging it is fully
// backward compatible - old nodes accept the blocks and just read version 1.
const (
	// NodeConsensusVersion is the consensus capability this binary signals in the
	// coinbase it builds. Bump it for each future rule change; the gate below then
	// measures adoption of the new value. v1 = original rules, v2 = fee market,
	// v3 = LWMA difficulty.
	NodeConsensusVersion = 3

	// Per-fork required versions are FROZEN constants and must NEVER reference the
	// moving NodeConsensusVersion: each fork's gate must keep measuring the exact
	// version that fork shipped with, or bumping the node version would silently
	// re-date an already-activated fork's activation height and split the network.
	FeeMarketVersion = 2 // fee market activates on >=2 signals (frozen)
	LWMAVersion      = 3 // LWMA difficulty activates on >=3 signals

	// SignalWindow / SignalThreshold: a rule locks in once at least <threshold> of
	// the last SignalWindow blocks signal >= the required version. SignalThreshold
	// (80/100) is the default used by the fee market. LWMA uses a HIGHER bar
	// (LWMAThreshold, 90/100): with our nodes at ~60% of hashrate, 90% is
	// unreachable until the ~40% external miner (rplant) is also on v3 - so the
	// threshold itself guarantees the big external pool upgraded before the
	// difficulty fork flips, with NO height floor needed (activation is purely
	// signal-driven, earliest at block SignalWindow).
	SignalWindow    = 100
	SignalThreshold = 80
	LWMAThreshold   = 90

	coinbaseTagPrefix = "crbnode/"
)

// coinbaseTag is the string a v-this node stamps into the coinbase Sig field so
// the block advertises its consensus version. Unvalidated and backward
// compatible: old nodes ignore the content entirely.
func coinbaseTag() string {
	return coinbaseTagPrefix + strconv.Itoa(NodeConsensusVersion)
}

// coinbaseVersion reads the consensus version a block advertises. Blocks built
// by old nodes (empty or genesis-message coinbase Sig) read as version 1.
func coinbaseVersion(b *Block) int {
	if len(b.Txs) == 0 {
		return 1
	}
	sig := b.Txs[0].Sig
	if !strings.HasPrefix(sig, coinbaseTagPrefix) {
		return 1
	}
	v, err := strconv.Atoi(sig[len(coinbaseTagPrefix):])
	if err != nil || v < 1 {
		return 1
	}
	return v
}

// signalCount returns how many of the SignalWindow blocks ending just before
// height `at` (i.e. blocks[at-SignalWindow : at]) advertise >= the required
// version. `blocks` is the chain prefix; at must be <= len(blocks).
func signalCount(blocks []*Block, at uint64, required int) int {
	if at < SignalWindow || at > uint64(len(blocks)) {
		return 0
	}
	var n int
	for h := at - SignalWindow; h < at; h++ {
		if coinbaseVersion(blocks[h]) >= required {
			n++
		}
	}
	return n
}

// activationHeight is the generic readiness gate: the FIRST height >= floor
// whose preceding SignalWindow blocks reached `threshold` signals for
// >= requiredVersion, or 0 if not locked in yet. Sticky (the returned height is
// the minimum qualifying one, computed from immutable history, so it never moves
// once reached) and deterministic from chain data alone. floor==0 means "no
// height floor" - activation is gated purely by the signal (earliest possible
// height is SignalWindow, since you need a full window to measure).
//
// FUTURE FORKS: reuse this. Bump NodeConsensusVersion, add a frozen <Name>Version
// const, and gate on activationHeight(blocks, <floor or 0>, <Name>Version,
// <threshold>). No new gate logic.
func activationHeight(blocks []*Block, floor uint64, requiredVersion, threshold int) uint64 {
	n := uint64(len(blocks))
	if n < floor {
		return 0
	}
	start := floor
	if start < SignalWindow {
		start = SignalWindow
	}
	for a := start; a <= n; a++ {
		if signalCount(blocks, a, requiredVersion) >= threshold {
			return a
		}
	}
	return 0
}

// feeMarketActivation returns the height at which the fee-market rule (flat fee
// floor + market block selection) locks in for this chain, or 0 if not yet.
// Measures FeeMarketVersion (frozen 2), NOT NodeConsensusVersion, so later forks
// bumping the node version cannot move this already-activated height.
func feeMarketActivation(blocks []*Block) uint64 {
	return activationHeight(blocks, FeeMarketHeight, FeeMarketVersion, SignalThreshold)
}

// feeMarketActiveAt reports whether the flat fee floor is in force for a block at
// `height`, given chain prefix `blocks` (the blocks before it).
func feeMarketActiveAt(blocks []*Block, height uint64) bool {
	a := feeMarketActivation(blocks)
	return a != 0 && height >= a
}

// lwmaActivation returns the height at which the LWMA difficulty rule locks in
// for this chain, or 0 if not yet. NO height floor (floor 0): activation is
// purely signal-driven and fires at the first block (>= SignalWindow) whose last
// 100 blocks carry >= LWMAThreshold (90) v3 signals. The 90% bar can't be met
// until the ~40% external pool is also on v3, so this self-protects against
// activating while a large miner is still on the old rules.
func lwmaActivation(blocks []*Block) uint64 {
	return activationHeight(blocks, 0, LWMAVersion, LWMAThreshold)
}

// lwmaActiveAt reports whether the LWMA retarget governs the block at `height`,
// given chain prefix `blocks`. Below activation the legacy retarget is used.
func lwmaActiveAt(blocks []*Block, height uint64) bool {
	a := lwmaActivation(blocks)
	return a != 0 && height >= a
}

func init() {
	// A signal threshold must be a real supermajority and fit the window, or the
	// gate is either trivially met or unreachable.
	if LWMAThreshold <= SignalWindow/2 || LWMAThreshold > SignalWindow {
		panic("LWMAThreshold must be a supermajority within (SignalWindow/2, SignalWindow]")
	}
}
