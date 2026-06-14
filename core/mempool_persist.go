package core

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
)

func (c *Chain) mempoolFile() string { return filepath.Join(c.dir, "mempool.json") }

// SaveMempool atomically writes the current mempool to disk so pending txns
// survive a node restart (Bitcoin keeps mempool.dat for exactly this reason).
// Without it, a restart silently drops everything in flight - which once caused
// already-counted pool payouts to vanish.
func (c *Chain) SaveMempool() error {
	c.mu.RLock()
	txs := make([]*Tx, 0, len(c.mempool))
	for _, t := range c.mempool {
		txs = append(txs, t)
	}
	c.mu.RUnlock()
	raw, err := json.Marshal(txs)
	if err != nil {
		return err
	}
	tmp := c.mempoolFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.mempoolFile()) // atomic replace
}

// LoadMempool restores a persisted mempool at startup, re-validating every tx
// against the current chain state (already-mined / now-invalid ones are simply
// dropped). Re-added in nonce order so per-sender chains rebuild correctly.
func (c *Chain) LoadMempool() {
	raw, err := os.ReadFile(c.mempoolFile())
	if err != nil {
		return
	}
	var txs []*Tx
	if json.Unmarshal(raw, &txs) != nil {
		return
	}
	sort.Slice(txs, func(i, j int) bool { return txs[i].Nonce < txs[j].Nonce })
	kept := 0
	for _, t := range txs {
		if c.AddTx(t) == nil {
			kept++
		}
	}
	if len(txs) > 0 {
		log.Printf("mempool: restored %d/%d pending txns from disk", kept, len(txs))
	}
}
