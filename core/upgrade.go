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
	// measures adoption of the new value. v1 = original rules, v2 = fee market.
	NodeConsensusVersion = 2

	// SignalWindow / SignalThreshold: the new rule locks in once at least
	// SignalThreshold of the last SignalWindow blocks signal >= the required
	// version. 80/100 is a clear supermajority tuned for a young network where a
	// single solo miner on an old node can hold 5-10% of blocks; it still ensures
	// the heavier chain is on the new rules so a minority left behind dies quickly.
	SignalWindow    = 100
	SignalThreshold = 80

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
// whose preceding SignalWindow blocks reached SignalThreshold signals for
// >= requiredVersion, or 0 if not locked in yet. Sticky (the returned height is
// the minimum qualifying one, computed from immutable history, so it never moves
// once reached) and deterministic from chain data alone.
//
// FUTURE FORKS: reuse this. Bump NodeConsensusVersion, add a `<Name>Height`
// activation floor const, and gate the new rule on
// activationHeight(blocks, <Name>Height, <newVersion>). No new gate logic.
//
// In practice the loop terminates within a few iterations of the floor: once the
// majority-hashrate pool/seed run the new binary, the window fills with signals
// and locks in almost immediately at the floor.
func activationHeight(blocks []*Block, floor uint64, requiredVersion int) uint64 {
	n := uint64(len(blocks))
	if n < floor {
		return 0
	}
	start := floor
	if start < SignalWindow {
		start = SignalWindow
	}
	for a := start; a <= n; a++ {
		if signalCount(blocks, a, requiredVersion) >= SignalThreshold {
			return a
		}
	}
	return 0
}

// feeMarketActivation returns the height at which the fee-market rule (flat fee
// floor + market block selection) locks in for this chain, or 0 if not yet.
func feeMarketActivation(blocks []*Block) uint64 {
	return activationHeight(blocks, FeeMarketHeight, NodeConsensusVersion)
}

// feeMarketActiveAt reports whether the flat fee floor is in force for a block at
// `height`, given chain prefix `blocks` (the blocks before it).
func feeMarketActiveAt(blocks []*Block, height uint64) bool {
	a := feeMarketActivation(blocks)
	return a != 0 && height >= a
}
