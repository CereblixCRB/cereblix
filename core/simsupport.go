package core

// MarkVerifiedForTest preseeds the PoW-verification cache for the given blocks.
//
// It exists solely so a CROSS-PACKAGE test harness (node/sim_test.go) can stand up
// an in-process testnet WITHOUT mining the memory-hard NeuroMorph proof-of-work for
// every block — exactly the shortcut in-package core tests already take by writing
// c.verifiedPow[b.Hash()] = true directly (see adopt_fastpath_test.go,
// preverify_test.go). Because verifiedPow is unexported, a test in package `node`
// cannot reach it; this method is the minimal seam that exposes that one mechanism.
//
// It records ONLY that the block hash was PoW-verified. Every other consensus check
// in validateBlock (height, prev-hash linkage, target == expectedTarget, txroot,
// coinbase, signatures, …) still runs unchanged, so it cannot make an otherwise
// invalid block valid — it just skips the re-hash. It has no effect on any
// production code path; nothing in the daemon calls it.
func (c *Chain) MarkVerifiedForTest(blocks ...*Block) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range blocks {
		c.markPowVerified(b.Hash())
	}
}
