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

// commitStallForTest, when non-nil, is invoked at the START of commitExtend/commitRebuild
// so a test can simulate a slow/stalled bbolt commit (fsync) on a sick disk. Since the
// RC5 fix these commits run under diskMu with the chain write lock c.mu RELEASED, so the
// hook lets a test prove a stalled commit no longer freezes c.mu readers (and that diskMu
// still orders concurrent writers). nil (a no-op) in production; the daemon never sets it.
var commitStallForTest func()

// SetCommitStallForTest installs (or clears, with nil) the commit-stall hook. Test-only.
func SetCommitStallForTest(f func()) { commitStallForTest = f }
