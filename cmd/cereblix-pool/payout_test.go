package main

import "testing"

// TestPlanPayoutsProportional is the user's example: two miners owed 1000 and 100,
// and only 110 spendable -> the budget splits 10/11 vs 1/11 (100 and 10).
func TestPlanPayoutsProportional(t *testing.T) {
	plan := planPayouts([]due{{"A", 1000}, {"B", 100}}, 110, 1)
	got := map[string]uint64{}
	var sum uint64
	for _, d := range plan {
		got[d.m] += d.amt
		sum += d.amt
	}
	if got["A"] != 100 || got["B"] != 10 {
		t.Fatalf("proportional split wrong: A=%d B=%d (want 100 and 10)", got["A"], got["B"])
	}
	if sum > 110 {
		t.Fatalf("overspent budget: %d > 110", sum)
	}
}

// When the budget covers everyone, each miner is paid their full owed.
func TestPlanPayoutsAbundant(t *testing.T) {
	plan := planPayouts([]due{{"A", 1000}, {"B", 100}}, 5000, 1)
	got := map[string]uint64{}
	for _, d := range plan {
		got[d.m] += d.amt
	}
	if got["A"] != 1000 || got["B"] != 100 {
		t.Fatalf("abundant budget should pay in full: A=%d B=%d", got["A"], got["B"])
	}
}

// Slices below the minimum payout are skipped (no dust / no wasted fee).
func TestPlanPayoutsDustSkipped(t *testing.T) {
	if plan := planPayouts([]due{{"A", 1000}, {"B", 100}}, 5, 10); len(plan) != 0 {
		t.Fatalf("sub-minPayout shares must be skipped, got %v", plan)
	}
}

// Never overspends the budget even with many miners and rounding.
func TestPlanPayoutsNeverOverspends(t *testing.T) {
	list := []due{{"A", 333}, {"B", 333}, {"C", 333}, {"D", 1}}
	var sum uint64
	for _, d := range planPayouts(list, 100, 1) {
		sum += d.amt
	}
	if sum > 100 {
		t.Fatalf("overspent: %d > 100", sum)
	}
}
