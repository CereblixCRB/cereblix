package node

import (
	"math/big"
	"strings"
	"testing"
)

func TestTipAhead(t *testing.T) {
	our := big.NewInt(0x1000) // ourWork
	ours := strings.Repeat("8", 64)
	hi := strings.Repeat("f", 64)
	lo := strings.Repeat("1", 64)
	cases := []struct {
		name    string
		cumWork string
		hash    string
		want    bool
	}{
		{"more work", "2000", hi, true},          // 0x2000 > 0x1000 -> pull regardless of hash
		{"less work", "800", lo, false},          // 0x800 < 0x1000 -> we lead
		{"equal, tie-break win", "1000", lo, true},   // equal work, smaller hash wins
		{"equal, tie-break lose", "1000", hi, false}, // equal work, larger hash
		{"equal, same tip", "1000", ours, false},     // the common announce-storm case
		{"garbage work", "zzz", lo, false},
		{"absurd length", strings.Repeat("f", 90), lo, false},
	}
	for _, c := range cases {
		if got := tipAhead(c.cumWork, c.hash, our, ours); got != c.want {
			t.Errorf("%s: tipAhead=%v want %v", c.name, got, c.want)
		}
	}
}
