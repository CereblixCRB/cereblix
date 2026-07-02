package core

import (
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// These tests guard the 2026-07-02 fix (node v2.4.3): a reorg is persisted
// INCREMENTALLY (truncateAndAppend, O(depth+branch)) instead of rewriting the
// whole chain in one bbolt transaction. The old full rewrite grew past the
// stall watchdog's restart interval on the ~30k-block chain, was rolled back
// on every restart, and pinned the on-disk chain at the losing pre-reorg tip
// forever (CORE/SG overnight restart loop: every start reloaded height 29793,
// re-ran the same depth-1 reorg and re-entered the same never-finishing txn).

// reorgScenarioBolt builds a bbolt-backed chain c with base+1 blocks (our tip is
// a branch that will LOSE) and a heavier 2-block candidate forking one below our
// tip, PoW-preseeded into c. Returns (c, candidate); adopting is a depth-1 reorg.
func reorgScenarioBolt(t *testing.T, dir string) (*Chain, []*Block) {
	t.Helper()
	addr := "crb1" + strings.Repeat("b", 40)
	c, err := OpenChain(dir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !c.UsingBolt() {
		t.Fatal("precondition: chain must be on the bbolt store")
	}
	base := drBuild(t, c, addr, 5) // heights 1..5
	drBuild(t, c, addr, 1)         // our losing tip, height 6

	fc, err := NewChain(t.TempDir()) // scratch fork-builder (jsonl, throwaway)
	if err != nil {
		t.Fatal(err)
	}
	drLoad(t, fc, base)
	// Different coinbase address → the branch GENUINELY differs at height 6
	// (same-address same-second templates would be byte-identical blocks).
	addr2 := "crb1" + strings.Repeat("d", 40)
	cand := drBuild(t, fc, addr2, 2) // heavier branch, heights 6..7
	for _, b := range cand {
		c.verifiedPow[b.Hash()] = true
	}
	return c, cand
}

// TestReorgCommitPersistsAcrossRestart is the money test for the incident: after
// a depth-1 reorg the ADOPTED branch must be on disk, so a restart reloads the
// post-reorg tip — not the losing one.
func TestReorgCommitPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	c, cand := reorgScenarioBolt(t, dir)
	oldTip := c.Tip().Hash()

	if err := c.TryAdoptChain(cand[0].Height, cand); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	wantTip := cand[len(cand)-1].Hash()
	if c.Tip().Hash() != wantTip {
		t.Fatalf("memory tip = %s, want adopted %s", c.Tip().Hash(), wantTip)
	}
	if err := c.store.close(); err != nil {
		t.Fatal(err)
	}

	c2, err := OpenChain(dir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.store.close()
	if got := c2.Tip().Hash(); got != wantTip {
		t.Fatalf("RESTART reloaded tip %s (height %d), want the ADOPTED %s — the reorg was not persisted",
			got, c2.Height(), wantTip)
	}
	if got := c2.Tip().Hash(); got == oldTip {
		t.Fatal("restart reloaded the LOSING pre-reorg tip — the overnight wedge scenario")
	}
	if c2.Height() != cand[len(cand)-1].Height {
		t.Fatalf("restart height = %d, want %d", c2.Height(), cand[len(cand)-1].Height)
	}
}

func dumpBucket(t *testing.T, db *bolt.DB, name []byte) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket(name)
		if bkt == nil {
			return nil
		}
		return bkt.ForEach(func(k, v []byte) error {
			m[string(k)] = string(v)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestTruncateAndAppendEquivalentToRebuild proves the incremental reorg commit
// leaves EXACTLY the store a from-scratch rebuild of the final chain would:
// same blocks, same blockHash/txIndex/addrTx indexes (no stale rows from the
// discarded branch, none missing), same meta.
func TestTruncateAndAppendEquivalentToRebuild(t *testing.T) {
	c, cand := reorgScenarioBolt(t, t.TempDir())
	if err := c.TryAdoptChain(cand[0].Height, cand); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	defer c.store.close()

	ref, err := openBlockStore(t.TempDir() + "/ref.db")
	if err != nil {
		t.Fatal(err)
	}
	defer ref.close()
	if err := ref.rebuild(c.blocks, c.cumWork); err != nil {
		t.Fatal(err)
	}

	for _, name := range [][]byte{bkBlocks, bkBlockHash, bkTxIndex, bkAddrTx, bkMeta} {
		got := dumpBucket(t, c.store.db, name)
		want := dumpBucket(t, ref.db, name)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("bucket %s diverges after incremental reorg commit:\n got %d keys\nwant %d keys",
				name, len(got), len(want))
		}
	}
}

// TestReorgCommitCleansOldBranchIndexes: the discarded block must vanish from
// every navigation index, and the adopted branch must be resolvable.
func TestReorgCommitCleansOldBranchIndexes(t *testing.T) {
	c, cand := reorgScenarioBolt(t, t.TempDir())
	oldTip := c.Tip()
	oldCoinbase := oldTip.Txs[0].ID()

	if err := c.TryAdoptChain(cand[0].Height, cand); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	defer c.store.close()

	if b := c.store.blockByHash(oldTip.Hash()); b != nil {
		t.Fatal("stale blockHash index row: the discarded tip is still resolvable")
	}
	if _, _, ok := c.store.findTxLoc(oldCoinbase); ok {
		t.Fatal("stale txIndex row: the discarded tip's coinbase is still indexed")
	}
	for _, b := range cand {
		if got := c.store.blockByHash(b.Hash()); got == nil {
			t.Fatalf("adopted block %d missing from blockHash index", b.Height)
		}
		if h, _, ok := c.store.findTxLoc(b.Txs[0].ID()); !ok || h != b.Height {
			t.Fatalf("adopted block %d coinbase not indexed (ok=%v h=%d)", b.Height, ok, h)
		}
	}
}

// TestCommitPanicBecomesError: a panic inside the disk commit must surface as a
// returned+logged error — NOT unwind past OnNewBlock (which silently froze the
// tip snapshot, RC6) — and must release diskMu so the next commit proceeds.
func TestCommitPanicBecomesError(t *testing.T) {
	c, err := OpenChain(t.TempDir(), true, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.store.close()
	addr := "crb1" + strings.Repeat("c", 40)
	drBuild(t, c, addr, 3)

	fired := false
	c.OnNewBlock = func(*Block) { fired = true }
	SetCommitStallForTest(func() { panic("injected commit panic") })
	b, err := c.BuildTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	c.verifiedPow[b.Hash()] = true
	err = c.AddBlock(b)
	SetCommitStallForTest(nil)
	if err == nil || !strings.Contains(err.Error(), "commit panic") {
		t.Fatalf("AddBlock with a panicking commit: got err=%v, want a 'commit panic' error", err)
	}
	if !fired {
		t.Fatal("OnNewBlock did not fire after a commit panic — tip snapshot would freeze (RC6 regression)")
	}

	// diskMu must have been released: a follow-up commit succeeds.
	b2, err := c.BuildTemplate(addr)
	if err != nil {
		t.Fatal(err)
	}
	c.verifiedPow[b2.Hash()] = true
	if err := c.AddBlock(b2); err != nil {
		t.Fatalf("commit after a recovered panic failed: %v — diskMu leaked?", err)
	}
}

// TestRebuildBatched: rebuild (startup-only path) must survive multiple batch
// boundaries and reproduce the exact block sequence. Uses synthetic blocks —
// the store layer does no consensus validation.
func TestRebuildBatched(t *testing.T) {
	const n = 2500 // > 2 batches of 1000
	blocks := synthChain(n, 0)
	st, err := openBlockStore(t.TempDir() + "/batched.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	if err := st.rebuild(blocks, cumWorkOf(t, blocks)); err != nil {
		t.Fatal(err)
	}
	tip, ok := st.tipHeight()
	if !ok || tip != n-1 {
		t.Fatalf("tipHeight = %d,%v; want %d", tip, ok, n-1)
	}
	i := 0
	if err := st.forEachBlock(func(b *Block) error {
		if b.Height != uint64(i) || b.Hash() != blocks[i].Hash() {
			return fmt.Errorf("block %d mismatch", i)
		}
		i++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if i != n {
		t.Fatalf("forEachBlock returned %d blocks, want %d", i, n)
	}
}

// synthChain builds n linked synthetic blocks (store-level tests only — the
// store layer does no consensus validation). Coinbase-style tx per block keeps
// the txIndex/addrTx buckets exercised.
func synthChain(n int, saltNonce uint64) []*Block {
	blocks := make([]*Block, n)
	prev := ""
	for i := 0; i < n; i++ {
		b := &Block{Height: uint64(i), Time: uint64(1000 + i), PrevHash: prev,
			Target: strings.Repeat("f", 64), Nonce: saltNonce + uint64(i),
			Txs: []*Tx{{To: fmt.Sprintf("crb1%040d", i), Amount: 1, Nonce: saltNonce + uint64(i)}}}
		blocks[i] = b
		prev = b.Hash()
	}
	return blocks
}

// TestTruncateAndAppendBatchedHugeBranch exercises the BATCHED path (branch >
// 1000 — a long-stranded node catching up across a fork / v4 deep recovery):
// the result must be byte-identical to a from-scratch rebuild of the final
// chain, with the discarded fork tail gone from every index.
func TestTruncateAndAppendBatchedHugeBranch(t *testing.T) {
	old := synthChain(50, 0) // heights 0..49 on disk
	st, err := openBlockStore(t.TempDir() + "/huge.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.close()
	if err := st.rebuild(old, cumWorkOf(t, old)); err != nil {
		t.Fatal(err)
	}

	// New branch forks at height 30 and runs 1500 blocks (heights 30..1529).
	branchAll := synthChain(1530, 7777) // different nonces -> different hashes
	final := append(append([]*Block{}, old[:30]...), branchAll[30:]...)
	if err := st.truncateAndAppend(30, branchAll[30:], cumWorkOf(t, final)); err != nil {
		t.Fatal(err)
	}

	tip, ok := st.tipHeight()
	if !ok || tip != 1529 {
		t.Fatalf("tipHeight = %d,%v; want 1529", tip, ok)
	}
	for _, b := range old[30:] { // discarded tail gone from all indexes
		if got := st.blockByHash(b.Hash()); got != nil {
			t.Fatalf("discarded block %d still resolvable by hash", b.Height)
		}
		if _, _, ok := st.findTxLoc(b.Txs[0].ID()); ok {
			t.Fatalf("discarded block %d tx still indexed", b.Height)
		}
	}
	ref, err := openBlockStore(t.TempDir() + "/href.db")
	if err != nil {
		t.Fatal(err)
	}
	defer ref.close()
	if err := ref.rebuild(final, cumWorkOf(t, final)); err != nil {
		t.Fatal(err)
	}
	for _, name := range [][]byte{bkBlocks, bkBlockHash, bkTxIndex, bkAddrTx, bkMeta} {
		got := dumpBucket(t, st.db, name)
		want := dumpBucket(t, ref.db, name)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("bucket %s diverges after batched truncateAndAppend (%d vs %d keys)", name, len(got), len(want))
		}
	}
}

func cumWorkOf(t *testing.T, blocks []*Block) *big.Int {
	t.Helper()
	w := new(big.Int)
	for _, b := range blocks {
		tg, err := b.TargetInt()
		if err != nil {
			t.Fatal(err)
		}
		w.Add(w, WorkOf(tg))
	}
	return w
}
