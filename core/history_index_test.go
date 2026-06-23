package core

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

// histBrute is the pre-index full-chain scan, kept as a reference oracle.
func histBrute(c *Chain, addr string, limit, offset int) []HistoryItem {
	var out []HistoryItem
	skipped := 0
	for i := len(c.blocks) - 1; i >= 0 && len(out) < limit; i-- {
		b := c.blocks[i]
		for _, t := range b.Txs {
			from := "coinbase"
			if !t.IsCoinbase() {
				from, _ = t.FromAddr()
			}
			if from == addr || t.To == addr {
				if skipped < offset {
					skipped++
					continue
				}
				out = append(out, HistoryItem{TxID: t.ID(), Height: b.Height, Time: b.Time, From: from, To: t.To, Amount: t.Amount, Fee: t.Fee})
				if len(out) >= limit {
					break
				}
			}
		}
	}
	return out
}

func sameHist(a, b []HistoryItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHistoryIndexMatchesScan guards the per-address index: indexed History must
// be byte-identical to the full-chain scan for every (addr, limit, offset), and
// the full rebuild (load / post-reorg path) must match the incremental one.
func TestHistoryIndexMatchesScan(t *testing.T) {
	c, err := NewChain(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	miner := "crb1" + strings.Repeat("a", 40)
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)
	A, B := AddrFromPub(pubA), AddrFromPub(pubB)
	c.state[A] = &Account{Balance: 1_000_000_000}
	c.state[B] = &Account{Balance: 1_000_000_000}
	c.recomputeSupplyLocked()

	mine := func() {
		b, err := c.BuildTemplate(miner)
		if err != nil {
			t.Fatalf("BuildTemplate: %v", err)
		}
		c.verifiedPow[b.Hash()] = true
		if err := c.AddBlock(b); err != nil {
			t.Fatalf("AddBlock: %v", err)
		}
	}
	send := func(priv ed25519.PrivateKey, to string, amt, nonce uint64) {
		tx := &Tx{To: to, Amount: amt, Fee: 1000, Nonce: nonce}
		SignTxAt(tx, priv, uint64(len(c.blocks)))
		if err := c.AddTx(tx); err != nil {
			t.Fatalf("AddTx: %v", err)
		}
	}

	send(privA, B, 100, 0)
	send(privA, A, 50, 1) // self-send
	mine()
	send(privB, A, 200, 0)
	send(privA, B, 10, 2)
	mine()
	mine() // coinbase-only block

	check := func(label, addr string) {
		for _, lo := range [][2]int{{50, 0}, {1, 0}, {1, 1}, {2, 1}, {3, 2}, {100, 0}} {
			got := c.History(addr, lo[0], lo[1])
			want := histBrute(c, addr, lo[0], lo[1])
			if !sameHist(got, want) {
				t.Fatalf("%s History(lim=%d,off=%d): index != scan\n index=%+v\n  scan=%+v", label, lo[0], lo[1], got, want)
			}
		}
	}
	check("A", A)
	check("B", B)
	check("miner", miner)

	// Full rebuild (simulates load / post-reorg) must yield the same index.
	c.mu.Lock()
	c.reindexAddrTxLocked()
	c.mu.Unlock()
	check("A/reindex", A)
	check("B/reindex", B)
	check("miner/reindex", miner)
}
