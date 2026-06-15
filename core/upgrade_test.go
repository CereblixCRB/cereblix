package core

import (
	"strconv"
	"testing"
)

func blkVer(v int) *Block {
	sig := ""
	if v >= 2 {
		sig = coinbaseTagPrefix + strconv.Itoa(v)
	}
	return &Block{Txs: []*Tx{{To: "x", Sig: sig}}}
}

func TestCoinbaseVersion(t *testing.T) {
	cases := map[string]int{
		"":             1, // old node / empty
		"crbnode/2":    2,
		"crbnode/3":    3,
		"deadbeef":     1, // genesis-style hex message
		"crbnode/oops": 1, // malformed
		"crbnode/0":    1, // out of range
	}
	for sig, want := range cases {
		b := &Block{Txs: []*Tx{{Sig: sig}}}
		if got := coinbaseVersion(b); got != want {
			t.Errorf("coinbaseVersion(%q) = %d, want %d", sig, got, want)
		}
	}
	if got := coinbaseVersion(blkVer(NodeConsensusVersion)); got != NodeConsensusVersion {
		t.Errorf("blkVer(NodeConsensusVersion) = %d, want %d", got, NodeConsensusVersion)
	}
}

func TestSignalCount(t *testing.T) {
	mk := func(v2 int) []*Block {
		bs := make([]*Block, SignalWindow)
		for i := range bs {
			if i < v2 {
				bs[i] = blkVer(2)
			} else {
				bs[i] = blkVer(1)
			}
		}
		return bs
	}
	if got := signalCount(mk(95), SignalWindow, 2); got != 95 {
		t.Fatalf("95 v2: got %d", got)
	}
	if got := signalCount(mk(94), SignalWindow, 2); got != 94 {
		t.Fatalf("94 v2: got %d", got)
	}
	// window shorter than required -> 0
	if got := signalCount(mk(95), SignalWindow-1, 2); got != 0 {
		t.Fatalf("short window: got %d", got)
	}
}

func TestFeeMarketActivationGate(t *testing.T) {
	L := FeeMarketHeight + 20

	// All v1: never activates.
	allV1 := make([]*Block, L)
	for i := range allV1 {
		allV1[i] = blkVer(1)
	}
	if a := feeMarketActivation(allV1); a != 0 {
		t.Fatalf("all-v1 must not activate, got %d", a)
	}
	if feeMarketActiveAt(allV1, FeeMarketHeight) {
		t.Fatal("all-v1 must be inactive at FeeMarketHeight")
	}

	// v2 from FeeMarketHeight-SignalWindow onward: the window ending at
	// FeeMarketHeight is 100% v2 -> locks in exactly at the floor.
	ready := make([]*Block, L)
	for i := range ready {
		if uint64(i) >= FeeMarketHeight-SignalWindow {
			ready[i] = blkVer(2)
		} else {
			ready[i] = blkVer(1)
		}
	}
	if a := feeMarketActivation(ready); a != FeeMarketHeight {
		t.Fatalf("ready chain must activate at %d, got %d", FeeMarketHeight, a)
	}
	if !feeMarketActiveAt(ready, FeeMarketHeight) {
		t.Fatal("must be active AT activation height")
	}
	if feeMarketActiveAt(ready, FeeMarketHeight-1) {
		t.Fatal("must be inactive one block before activation")
	}

	// Stickiness: once locked in, a later run of v1 blocks does not deactivate.
	for i := FeeMarketHeight; uint64(i) < uint64(L); i++ {
		ready[i] = blkVer(1)
	}
	if a := feeMarketActivation(ready); a != FeeMarketHeight {
		t.Fatalf("activation must stay sticky at %d, got %d", FeeMarketHeight, a)
	}
	if !feeMarketActiveAt(ready, uint64(L-1)) {
		t.Fatal("fee market must remain active after lock-in despite v1 tail")
	}
}
