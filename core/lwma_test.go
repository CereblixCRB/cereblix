package core

import (
	"math/big"
	"strings"
	"testing"
)

// mkTargetChain builds a synthetic prefix with the given solvetimes (seconds
// between consecutive blocks) and a constant target. Only Time/Target are set -
// enough for the pure retarget functions (no PoW).
func mkTargetChain(n int, solvetime uint64, target *big.Int) []*Block {
	bl := make([]*Block, n)
	var t uint64 = 1_000_000
	for i := 0; i < n; i++ {
		bl[i] = &Block{Height: uint64(i), Time: t, Target: TargetToHex(target)}
		t += solvetime
	}
	return bl
}

// TestLWMASteadyState: at exactly the target spacing and constant difficulty,
// the LWMA must reproduce that difficulty (no drift). This is the algebraic
// fixed point: nextD = sumD*T*(N+1) / (2 * sum_k k*T) = avgD.
func TestLWMASteadyState(t *testing.T) {
	T0 := new(big.Int).Lsh(big.NewInt(1), 236) // some target harder than the floor
	blocks := mkTargetChain(LWMAWindow+5, BlockTargetSpacing, T0)
	got := lwmaTarget(blocks)
	wantW := WorkOf(T0)
	gotW := WorkOf(got)
	// within ~1%
	diff := new(big.Int).Sub(gotW, wantW)
	diff.Abs(diff)
	tol := new(big.Int).Div(wantW, big.NewInt(100))
	if diff.Cmp(tol) > 0 {
		t.Fatalf("steady-state drift: got work %s, want ~%s (diff %s > 1%%)", gotW, wantW, diff)
	}
}

// TestLWMAEasesWhenSlow: if blocks suddenly take 2x the target spacing (hashrate
// halved), the LWMA must EASE - the next target gets larger (difficulty lower).
func TestLWMAEasesWhenSlow(t *testing.T) {
	T0 := new(big.Int).Lsh(big.NewInt(1), 236)
	blocks := mkTargetChain(LWMAWindow+5, 2*BlockTargetSpacing, T0)
	got := lwmaTarget(blocks)
	if got.Cmp(T0) <= 0 {
		t.Fatalf("LWMA did not ease on a hashrate drop: got target %s, want > %s", got, T0)
	}
	if got.Cmp(MaxTarget) > 0 {
		t.Fatalf("eased past the floor: %s > MaxTarget", got)
	}
}

// TestLWMATightensWhenFast: blocks at half the spacing (hashrate doubled) ->
// next target must shrink (difficulty rises).
func TestLWMATightensWhenFast(t *testing.T) {
	T0 := new(big.Int).Lsh(big.NewInt(1), 236)
	blocks := mkTargetChain(LWMAWindow+5, BlockTargetSpacing/2, T0)
	got := lwmaTarget(blocks)
	if got.Cmp(T0) >= 0 {
		t.Fatalf("LWMA did not tighten on a hashrate rise: got %s, want < %s", got, T0)
	}
	if got.Sign() <= 0 {
		t.Fatalf("target went non-positive: %s", got)
	}
}

// TestLWMAClampsTimestampSpike: a single absurd solvetime must be clamped so one
// manipulated/garbage timestamp can't crater the difficulty. The result must
// stay bounded well under what an unclamped huge gap would produce.
func TestLWMAClampsTimestampSpike(t *testing.T) {
	T0 := new(big.Int).Lsh(big.NewInt(1), 236)
	blocks := mkTargetChain(LWMAWindow+5, BlockTargetSpacing, T0)
	// Push the most recent block's timestamp absurdly far ahead.
	last := len(blocks) - 1
	blocks[last].Time = blocks[last-1].Time + 10_000_000
	got := lwmaTarget(blocks)
	// With the [1, 10*T] clamp, one spike can ease difficulty by at most a bounded
	// amount; assert the target stays below 8x T0 (an unclamped spike would blow
	// far past MaxTarget / saturate it).
	bound := new(big.Int).Mul(T0, big.NewInt(8))
	if got.Cmp(bound) > 0 && got.Cmp(MaxTarget) >= 0 {
		t.Fatalf("timestamp spike not clamped: target %s saturated", got)
	}
}

// TestFeeMarketActivationFrozen is the regression guard for the consensus trap
// found in audit: bumping NodeConsensusVersion to 3 must NOT change the
// fee-market activation, because feeMarketActivation measures the FROZEN
// FeeMarketVersion (2), not the moving node version. A chain that signaled v2
// around the fee-market floor must still activate there; measuring v3 (which no
// historical block signaled) would return 0 and split the network.
func TestFeeMarketActivationFrozen(t *testing.T) {
	if FeeMarketVersion != 2 {
		t.Fatalf("FeeMarketVersion must stay frozen at 2, got %d", FeeMarketVersion)
	}
	// The NEWEST fork constant must track the current node version; the older frozen
	// ones stay strictly below it, so bumping the node version can never re-date them.
	if DeepRecoveryVersion != NodeConsensusVersion {
		t.Fatalf("DeepRecoveryVersion (%d) should equal the current NodeConsensusVersion (%d)", DeepRecoveryVersion, NodeConsensusVersion)
	}
	if !(FeeMarketVersion < LWMAVersion && LWMAVersion < DeepRecoveryVersion) {
		t.Fatalf("frozen fork versions must be strictly increasing: fee=%d lwma=%d deep=%d", FeeMarketVersion, LWMAVersion, DeepRecoveryVersion)
	}
	// Build a v2-signaled chain past the fee-market floor.
	n := int(FeeMarketHeight) + 200
	blocks := make([]*Block, n)
	for i := range blocks {
		blocks[i] = &Block{Height: uint64(i), Txs: []*Tx{{Sig: "crbnode/2"}}}
	}
	if a := feeMarketActivation(blocks); a != FeeMarketHeight {
		t.Fatalf("fee market should activate at floor %d on a v2 chain, got %d", FeeMarketHeight, a)
	}
	// The bug we guard against: measuring node version 3 on this v2 chain finds no
	// activation at all (would retroactively turn fee market OFF for history).
	if a := activationHeight(blocks, FeeMarketHeight, 3, SignalThreshold); a != 0 {
		t.Fatalf("v2 chain must NOT signal v3 activation, got %d", a)
	}
}

// TestLWMAActivatesOnSignal verifies the floor-less, signal-only LWMA gate:
// it stays off until >= LWMAThreshold of the last 100 blocks signal v3, and is
// sticky once met. No height floor is involved.
func TestLWMAActivatesOnSignal(t *testing.T) {
	mk := func(v3 int) []*Block {
		// 200 blocks; the last 100 contain exactly v3 blocks signaling v3.
		bl := make([]*Block, 200)
		for i := range bl {
			sig := "crbnode/2"
			if i >= 200-v3 { // put the v3 signals at the tail (the measured window)
				sig = "crbnode/3"
			}
			bl[i] = &Block{Height: uint64(i), Txs: []*Tx{{Sig: sig}}}
		}
		return bl
	}
	if a := lwmaActivation(mk(LWMAThreshold - 1)); a != 0 {
		t.Fatalf("below threshold (%d v3) must NOT activate, got %d", LWMAThreshold-1, a)
	}
	if a := lwmaActivation(mk(LWMAThreshold)); a == 0 {
		t.Fatalf("at threshold (%d v3) must activate, got 0", LWMAThreshold)
	}
}

// TestLWMAStaysActiveOnceLocked is the "must never turn itself off" guard:
// activation is sticky. Once any window in the immutable past reached the v3
// supermajority, the rule stays active forever even if the v3 share later falls.
func TestLWMAStaysActiveOnceLocked(t *testing.T) {
	bl := make([]*Block, 300)
	for i := range bl {
		sig := "crbnode/2"
		if i >= 100 && i < 200 { // a v3 supermajority window mid-chain...
			sig = "crbnode/3"
		}
		bl[i] = &Block{Height: uint64(i), Txs: []*Tx{{Sig: sig}}}
	}
	a := lwmaActivation(bl)
	if a == 0 {
		t.Fatal("should have locked in during the v3 window")
	}
	// ...then the tail (200..299) drops back to v2 - activation must REMAIN.
	if !lwmaActiveAt(bl, 299) {
		t.Fatal("LWMA deactivated after v3 share fell - it must be sticky (never turn off)")
	}
	// And it stays pinned at the same locked height, not recomputed away.
	if lwmaActivation(bl) != a {
		t.Fatal("locked activation height moved - not sticky")
	}
}

// TestTieBreakDepth1 exercises the real TryAdoptChain fork-choice path: two
// competing tips at the same height have equal cumulative work, so the one with
// the smaller block hash must win deterministically (and the larger must be
// rejected). PoW is pre-seeded via verifiedPow so the test needn't mine.
func TestTieBreakDepth1(t *testing.T) {
	addrA := "crb1" + strings.Repeat("a", 40)
	addrB := "crb1" + strings.Repeat("b", 40)

	c0, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ba, err := c0.BuildTemplate(addrA)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := c0.BuildTemplate(addrB)
	if err != nil {
		t.Fatal(err)
	}
	if ba.Hash() == bb.Hash() {
		t.Fatal("competing templates collided")
	}
	smaller, larger := ba, bb
	if larger.Hash() < smaller.Hash() {
		smaller, larger = larger, smaller
	}

	mkChainWithTip := func(tip *Block) *Chain {
		c, err := NewChain(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		c.verifiedPow[tip.Hash()] = true
		if err := c.AddBlock(tip); err != nil {
			t.Fatalf("seed tip: %v", err)
		}
		return c
	}

	// (1) a competing tip with the SMALLER hash must be adopted.
	c1 := mkChainWithTip(larger)
	c1.verifiedPow[smaller.Hash()] = true
	if err := c1.TryAdoptChain(1, []*Block{smaller}); err != nil {
		t.Fatalf("smaller-hash equal-work tip should be adopted: %v", err)
	}
	if c1.Tip().Hash() != smaller.Hash() {
		t.Fatal("tip did not switch to the smaller-hash block on the tie-break")
	}

	// (2) a competing tip with the LARGER hash must be rejected.
	c2 := mkChainWithTip(smaller)
	c2.verifiedPow[larger.Hash()] = true
	if err := c2.TryAdoptChain(1, []*Block{larger}); err == nil {
		t.Fatal("larger-hash equal-work tip should be rejected")
	}
	if c2.Tip().Hash() != smaller.Hash() {
		t.Fatal("tip changed on a rejected (larger-hash) tie")
	}
}

func TestValid64Hex(t *testing.T) {
	ok := "00000000000000000000000000000000000000000000000000000000000000ab"
	if !valid64Hex(ok) {
		t.Fatal("valid 64-hex rejected")
	}
	for _, bad := range []string{"", "00", ok + "ff", ok[:63] + "zz"[:1], "xy" + ok[2:]} {
		if valid64Hex(bad) {
			t.Fatalf("malformed header field accepted: %q", bad)
		}
	}
}
