package core

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	nm "cereblix/neuromorph"
)

type Account struct {
	Balance uint64 `json:"balance"`
	Nonce   uint64 `json:"nonce"`
}

type State map[string]*Account

func (s State) get(addr string) *Account {
	a := s[addr]
	if a == nil {
		a = &Account{}
		s[addr] = a
	}
	return a
}

// Chain is the consensus engine: main chain, account state and mempool.
type Chain struct {
	mu      sync.RWMutex
	dir     string
	blocks  []*Block
	state   State
	cumWork *big.Int
	supply  uint64 // running Σ balances, refreshed under the write lock per block (Supply() O(1))

	// totals is a running per-address lifetime ledger (received/mined/sent/txn)
	// kept in sync with state so /api/balance answers in O(1) instead of an
	// O(chain) full scan on every request (which was a DoS amplification vector).
	totals map[string]*addrTotals

	mempool     map[string]*Tx
	verifiedPow map[string]bool

	paramsCache map[uint64]*nm.Params // epoch -> params
	vmCache     map[uint64]*nm.VM     // epoch -> validation VM
	vmSeed      map[uint64]string     // epoch -> seed(hex) the cached VM/params were built for; guards a reorg that changes a boundary block's hash (→ a new epoch seed)

	// 51%-resistance knobs (decentralized, no trusted party).
	// MaxReorgDepth rejects any reorg that would replace more than this many
	// of our own blocks; 0 disables the cap. ReorgPenaltyPermille makes deep
	// reorgs cost disproportionately more work: a candidate must exceed our
	// work by (depth * permille / 1000); 0 disables the penalty.
	MaxReorgDepth        uint64
	ReorgPenaltyPermille uint64

	// Checkpoints is an OPTIONAL, off-by-default break-glass against deep
	// attacks: height -> block hash. Empty = fully decentralized. When set,
	// the chain refuses any history that conflicts with a checkpoint.
	Checkpoints map[uint64]string

	OnNewBlock func(b *Block) // called outside lock after a block is adopted
}

func NewChain(dir string) (*Chain, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	c := &Chain{
		dir:         dir,
		state:       State{},
		totals:      map[string]*addrTotals{},
		cumWork:     new(big.Int),
		mempool:     map[string]*Tx{},
		verifiedPow: map[string]bool{},
		paramsCache: map[uint64]*nm.Params{},
		vmCache:     map[uint64]*nm.VM{},
		vmSeed:      map[uint64]string{},
		// Sane defaults: cap deep rewrites at 100 blocks (~1h40m at 60s),
		// no work penalty, no checkpoints. All overridable by the node.
		MaxReorgDepth:        100,
		ReorgPenaltyPermille: 0,
		Checkpoints:          map[uint64]string{},
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	c.loadCheckpoints()
	c.LoadMempool() // restore pending txns dropped by the restart
	return c, nil
}

// ------------------------------------------------------------- persistence

func (c *Chain) blocksFile() string { return filepath.Join(c.dir, "blocks.jsonl") }

func (c *Chain) load() error {
	f, err := os.Open(c.blocksFile())
	if errors.Is(err, os.ErrNotExist) {
		g := GenesisBlock()
		c.blocks = []*Block{g}
		c.verifiedPow[g.Hash()] = true
		c.rebuildDerived()
		return c.saveAll()
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var b Block
		if err := json.Unmarshal(sc.Bytes(), &b); err != nil {
			return fmt.Errorf("corrupt block store: %w", err)
		}
		c.blocks = append(c.blocks, &b)
		c.verifiedPow[b.Hash()] = true // trusted: we validated before writing
	}
	if len(c.blocks) == 0 {
		g := GenesisBlock()
		c.blocks = []*Block{g}
		c.verifiedPow[g.Hash()] = true
	}
	if c.blocks[0].Hash() != GenesisBlock().Hash() {
		return errors.New("block store has wrong genesis")
	}
	c.rebuildDerived()
	return nil
}

func (c *Chain) saveAll() error {
	tmp := c.blocksFile() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, b := range c.blocks {
		raw, _ := json.Marshal(b)
		w.Write(raw)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, c.blocksFile())
}

func (c *Chain) appendToDisk(b *Block) error {
	f, err := os.OpenFile(c.blocksFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, _ := json.Marshal(b)
	raw = append(raw, '\n')
	_, err = f.Write(raw)
	return err
}

// rebuildDerived recomputes state and cumulative work from c.blocks.
func (c *Chain) rebuildDerived() {
	st := State{}
	tot := map[string]*addrTotals{}
	work := new(big.Int)
	for _, b := range c.blocks {
		applyBlockToState(st, b)
		applyBlockToTotals(tot, b)
		t, _ := b.TargetInt()
		work.Add(work, WorkOf(t))
	}
	c.state = st
	c.totals = tot
	c.cumWork = work
	c.recomputeSupplyLocked()
}

// ------------------------------------------------------------ state rules

// immatureCoinbase sums, per address, the block rewards not yet spendable at
// block height `atHeight`: a coinbase mined at height H matures only when
// atHeight - H >= CoinbaseMaturity. Empty below MaturityHeight, where the rule
// is not yet enforced (so pre-activation blocks stay valid).
func immatureCoinbase(blocks []*Block, atHeight uint64) map[string]uint64 {
	imm := map[string]uint64{}
	if atHeight < MaturityHeight {
		return imm
	}
	lo := uint64(0)
	if atHeight > CoinbaseMaturity {
		lo = atHeight - CoinbaseMaturity + 1
	}
	for h := lo; h < atHeight && h < uint64(len(blocks)); h++ {
		b := blocks[h]
		if len(b.Txs) > 0 && b.Txs[0].IsCoinbase() {
			imm[b.Txs[0].To] += b.Txs[0].Amount
		}
	}
	return imm
}

// spendable returns an account's balance minus its still-immature coinbase.
func spendable(bal uint64, immature uint64) uint64 {
	if immature >= bal {
		return 0
	}
	return bal - immature
}

// validateTxAgainstState checks a non-coinbase tx against a state snapshot.
// `immature` maps address -> coinbase amount not yet matured at this height.
func validateTxAgainstState(st State, t *Tx, immature map[string]uint64, height uint64) error {
	if err := t.CheckSigAt(height); err != nil {
		return err
	}
	from, _ := t.FromAddr()
	acc := st.get(from)
	if t.Nonce != acc.Nonce {
		return fmt.Errorf("bad nonce: want %d got %d", acc.Nonce, t.Nonce)
	}
	total := t.Amount + t.Fee
	if total < t.Amount { // overflow
		return errors.New("amount overflow")
	}
	if spendable(acc.Balance, immature[from]) < total {
		return errors.New("insufficient spendable balance (coinbase not matured)")
	}
	return nil
}

func applyBlockToState(st State, b *Block) {
	for _, t := range b.Txs {
		if t.IsCoinbase() {
			st.get(t.To).Balance += t.Amount
			continue
		}
		from, _ := t.FromAddr()
		acc := st.get(from)
		acc.Balance -= t.Amount + t.Fee
		acc.Nonce++
		st.get(t.To).Balance += t.Amount
	}
}

// addrTotals is a running lifetime ledger for one address.
type addrTotals struct {
	Received uint64
	Mined    uint64
	Sent     uint64
	Txn      int
}

// clone deep-copies the account map so a candidate can be validated/applied
// without mutating the live state until it is committed (fast-path atomicity).
func (s State) clone() State {
	cp := make(State, len(s))
	for k, v := range s {
		a := *v
		cp[k] = &a
	}
	return cp
}

func cloneTotals(t map[string]*addrTotals) map[string]*addrTotals {
	cp := make(map[string]*addrTotals, len(t))
	for k, v := range t {
		a := *v
		cp[k] = &a
	}
	return cp
}

// applyBlockToTotals folds one block's transactions into the running totals map,
// mirroring the per-tx accounting AddrTotals used to compute by full scan.
func applyBlockToTotals(tot map[string]*addrTotals, b *Block) {
	get := func(a string) *addrTotals {
		t := tot[a]
		if t == nil {
			t = &addrTotals{}
			tot[a] = t
		}
		return t
	}
	for _, t := range b.Txs {
		if t.IsCoinbase() {
			to := get(t.To)
			to.Received += t.Amount
			to.Mined += t.Amount
			to.Txn++
			continue
		}
		from, _ := t.FromAddr()
		f := get(from)
		f.Sent += t.Amount + t.Fee
		f.Txn++
		if t.To != from {
			to := get(t.To)
			to.Received += t.Amount
			to.Txn++
		}
	}
}

// ---------------------------------------------------------------- targets

// bigTwo256 is 2^256, used to convert between a target and its work/difficulty.
var bigTwo256 = new(big.Int).Lsh(big.NewInt(1), 256)

// expectedTarget computes the required target for the block at height
// len(blocks). Once the LWMA fork activates (readiness-gated on the v3 signal,
// see lwmaActivation) it uses the LWMA retarget; before activation, the legacy
// windowed-average retarget - byte-for-byte unchanged so pre-activation history
// and blocks stay valid on every node.
func expectedTarget(blocks []*Block) *big.Int {
	if lwmaActiveAt(blocks, uint64(len(blocks))) {
		return lwmaTarget(blocks)
	}
	return legacyTarget(blocks)
}

// legacyTarget is the original windowed-average retarget (before LWMA activation).
func legacyTarget(blocks []*Block) *big.Int {
	h := len(blocks)
	if h < 2 {
		return new(big.Int).Set(GenesisTarget)
	}
	window := RetargetWindow
	if h-1 < window {
		window = h - 1
	}
	first := blocks[h-1-window]
	last := blocks[h-1]
	expected := int64(window * BlockTargetSpacing)
	actual := int64(last.Time) - int64(first.Time)
	if actual < expected/4 {
		actual = expected / 4
	}
	if actual > expected*4 {
		actual = expected * 4
	}
	sum := new(big.Int)
	for i := h - window; i < h; i++ {
		t, _ := blocks[i].TargetInt()
		sum.Add(sum, t)
	}
	avg := sum.Div(sum, big.NewInt(int64(window)))
	next := avg.Mul(avg, big.NewInt(actual))
	next.Div(next, big.NewInt(expected))
	if next.Cmp(MaxTarget) > 0 {
		next.Set(MaxTarget)
	}
	if next.Sign() <= 0 {
		next.SetInt64(1)
	}
	return next
}

// lwmaTarget computes the next target via LWMA-1 (Linearly Weighted Moving
// Average of solvetimes): recent blocks weigh more, so difficulty tracks
// hashrate swings fast without the legacy windowed average's oscillation. Pure
// integer math (big.Int + int64) so every node derives the identical target.
// Window = LWMAWindow (90); each solvetime is clamped to [1, 10*spacing] to bound
// timestamp noise/manipulation without clipping honest gaps.
func lwmaTarget(blocks []*Block) *big.Int {
	h := len(blocks)
	N := LWMAWindow
	if h < N+1 {
		return legacyTarget(blocks) // not enough history for a full window yet
	}
	const T = BlockTargetSpacing
	const stMax = 10 * T
	sumD := new(big.Int)
	var weighted int64 // sum_{k=1..N} k * solvetime_k
	for k := 1; k <= N; k++ {
		idx := h - N + (k - 1) // window blocks: indices h-N .. h-1
		st := int64(blocks[idx].Time) - int64(blocks[idx-1].Time)
		if st < 1 {
			st = 1
		}
		if st > stMax {
			st = stMax
		}
		weighted += int64(k) * st
		d, _ := blocks[idx].TargetInt()
		sumD.Add(sumD, WorkOf(d))
	}
	if weighted < 1 {
		weighted = 1
	}
	// nextDifficulty = sumD * T * (N+1) / (2 * weighted)
	nextD := new(big.Int).Mul(sumD, big.NewInt(int64(T*(N+1))))
	nextD.Div(nextD, big.NewInt(2*weighted))
	if nextD.Sign() <= 0 {
		nextD.SetInt64(1)
	}
	// target = 2^256 / nextDifficulty - 1 (inverse of WorkOf), clamped to floor.
	next := new(big.Int).Div(bigTwo256, nextD)
	next.Sub(next, big.NewInt(1))
	if next.Cmp(MaxTarget) > 0 {
		next.Set(MaxTarget)
	}
	if next.Sign() <= 0 {
		next.SetInt64(1)
	}
	return next
}

func medianTime(blocks []*Block) uint64 {
	n := 11
	if len(blocks) < n {
		n = len(blocks)
	}
	times := make([]uint64, 0, n)
	for _, b := range blocks[len(blocks)-n:] {
		times = append(times, b.Time)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2]
}

// ----------------------------------------------------------------- epochs

// epochSeedFor returns the NeuroMorph seed for a block at `height`, taken
// from the supplied chain prefix (which must reach the epoch boundary).
func epochSeedFor(blocks []*Block, height uint64) ([]byte, uint64) {
	epoch := height / EpochLength
	if epoch == 0 {
		return nm.EpochSeed0(), 0
	}
	boundary := epoch*EpochLength - 1
	raw, _ := hex.DecodeString(blocks[boundary].Hash())
	return raw, epoch
}

func (c *Chain) vmFor(blocks []*Block, height uint64) *nm.VM {
	seed, epoch := epochSeedFor(blocks, height)
	sk := hex.EncodeToString(seed)
	// Cache hit ONLY if the cached entry was built for the SAME seed. The seed is
	// the boundary block's hash, so a reorg that replaces a boundary block changes
	// the seed → the stale entry is rebuilt. This content-key makes the cache
	// correct without clearing it on every adopt (which forced needless VM
	// re-allocation, and risked a 64MiB dataset regen).
	if vm, ok := c.vmCache[epoch]; ok && c.vmSeed[epoch] == sk {
		return vm
	}
	p := nm.DeriveParams(seed)
	vm := nm.NewVM(p)
	c.paramsCache[epoch] = p
	c.vmCache[epoch] = vm
	c.vmSeed[epoch] = sk
	if len(c.vmCache) > 3 { // keep only recent epochs
		for e := range c.vmCache {
			if e+2 < epoch {
				delete(c.vmCache, e)
				delete(c.paramsCache, e)
				delete(c.vmSeed, e)
			}
		}
	}
	return vm
}

// EpochSeedForNext returns seed bytes + epoch for the next block (mining).
func (c *Chain) EpochSeedForNext() ([]byte, uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return epochSeedFor(c.blocks, uint64(len(c.blocks)))
}

// ------------------------------------------------------------- validation

// validateBlock fully validates b as the next block of `prefix`.
// st must be the state after applying `prefix`. PoW is skipped for hashes
// already in verifiedPow.
func (c *Chain) validateBlock(prefix []*Block, st State, b *Block) error {
	prev := prefix[len(prefix)-1]
	if b.Version != 1 {
		return errors.New("bad version")
	}
	if b.Height != uint64(len(prefix)) {
		return fmt.Errorf("bad height %d, want %d", b.Height, len(prefix))
	}
	// Header hash fields must be exactly 32 bytes of hex; otherwise HeaderBytes
	// would silently zero-pad/truncate them and two distinct blocks could share a
	// hash. (Canonical equality checks below also enforce this indirectly.)
	if !valid64Hex(b.PrevHash) || !valid64Hex(b.TxRoot) || !valid64Hex(b.Target) {
		return errors.New("malformed header field")
	}
	if cp, ok := c.Checkpoints[b.Height]; ok && cp != b.Hash() {
		return fmt.Errorf("block %d conflicts with authority checkpoint", b.Height)
	}
	if b.PrevHash != prev.Hash() {
		return errors.New("prev hash mismatch")
	}
	if b.Time <= medianTime(prefix) {
		return errors.New("timestamp too old")
	}
	if b.Time > uint64(time.Now().Unix()+MaxFutureDrift) {
		return errors.New("timestamp too far in future")
	}
	want := expectedTarget(prefix)
	got, err := b.TargetInt()
	if err != nil {
		return err
	}
	if want.Cmp(got) != 0 {
		return errors.New("wrong difficulty target")
	}
	if len(b.Txs) == 0 || len(b.Txs) > MaxBlockTxs {
		return errors.New("bad tx count")
	}
	if !b.Txs[0].IsCoinbase() {
		return errors.New("first tx must be coinbase")
	}
	if b.TxRoot != ComputeTxRoot(b.Txs) {
		return errors.New("tx root mismatch")
	}
	// Coinbase rules.
	cb := b.Txs[0]
	if cb.Nonce != b.Height {
		return errors.New("coinbase nonce must equal height")
	}
	if !ValidAddr(cb.To) {
		return errors.New("bad coinbase address")
	}
	if cb.Amount != BlockSubsidy(b.Height)+b.TotalFees() {
		return errors.New("bad coinbase amount")
	}
	// Body txs against a state copy.
	work := State{}
	for k, v := range st {
		cp := *v
		work[k] = &cp
	}
	imm := immatureCoinbase(prefix, b.Height)
	var minfee uint64
	if b.Height >= MinFeeHeight {
		minfee = minFeeFor(prefix)
	}
	seen := map[string]bool{}
	for _, t := range b.Txs[1:] {
		if t.IsCoinbase() {
			return errors.New("extra coinbase")
		}
		if seen[t.ID()] {
			return errors.New("duplicate tx in block")
		}
		seen[t.ID()] = true
		if t.Fee < minfee {
			return fmt.Errorf("tx %s: fee %d below minimum %d", t.ID()[:16], t.Fee, minfee)
		}
		if err := validateTxAgainstState(work, t, imm, b.Height); err != nil {
			return fmt.Errorf("tx %s: %w", t.ID()[:16], err)
		}
		from, _ := t.FromAddr()
		acc := work.get(from)
		acc.Balance -= t.Amount + t.Fee
		acc.Nonce++
		work.get(t.To).Balance += t.Amount
	}
	// Proof of work.
	if !c.verifiedPow[b.Hash()] {
		vm := c.vmFor(prefix, b.Height)
		pow := vm.Hash(b.HeaderBytes(), b.Height)
		if !HashMeetsTarget(pow, got) {
			return errors.New("insufficient proof of work")
		}
		c.verifiedPow[b.Hash()] = true
	}
	return nil
}

// AddBlock validates and appends a block extending the current tip.
func (c *Chain) AddBlock(b *Block) error {
	c.mu.Lock()
	if b.PrevHash != c.blocks[len(c.blocks)-1].Hash() {
		c.mu.Unlock()
		return errors.New("not extending tip")
	}
	if err := c.validateBlock(c.blocks, c.state, b); err != nil {
		c.mu.Unlock()
		return err
	}
	c.blocks = append(c.blocks, b)
	applyBlockToState(c.state, b)
	applyBlockToTotals(c.totals, b)
	c.recomputeSupplyLocked()
	t, _ := b.TargetInt()
	c.cumWork.Add(c.cumWork, WorkOf(t))
	for _, tx := range b.Txs {
		delete(c.mempool, tx.ID())
	}
	c.pruneMempoolLocked()
	err := c.appendToDisk(b)
	cb := c.OnNewBlock
	c.mu.Unlock()
	if cb != nil {
		cb(b)
	}
	return err
}

// TryAdoptChain attempts a reorg: candidate blocks start at startHeight and
// must connect to our chain there. Adopts only if cumulative work is higher.
func (c *Chain) TryAdoptChain(startHeight uint64, newBlocks []*Block) error {
	if len(newBlocks) == 0 {
		return errors.New("empty candidate")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if startHeight == 0 || startHeight > uint64(len(c.blocks)) {
		return errors.New("bad fork point")
	}

	// Decentralized 51% guard #1: reject reorgs that rewrite too much history.
	// depth = how many of our own blocks would be discarded.
	depth := uint64(len(c.blocks)) - startHeight
	if c.MaxReorgDepth > 0 && depth > c.MaxReorgDepth {
		return fmt.Errorf("reorg too deep: %d blocks (cap %d)", depth, c.MaxReorgDepth)
	}

	// Break-glass guard: never reorg below or across a configured checkpoint.
	if len(c.Checkpoints) > 0 {
		for h := range c.Checkpoints {
			if h >= startHeight {
				return fmt.Errorf("reorg conflicts with checkpoint at height %d", h)
			}
		}
	}

	// Cheap header-only candidate work (NO state replay): start from our cumWork,
	// subtract the blocks we would disconnect, add the candidate's. This equals a
	// full replay's work sum EXACTLY (same WorkOf terms), so the fork-choice is
	// identical — but O(depth+newBlocks) instead of O(chain). Redundant/losing
	// adopts (the common case: many peers re-offer the block we just took) bail
	// here before any expensive work.
	candWork := new(big.Int).Set(c.cumWork)
	for i := startHeight; i < uint64(len(c.blocks)); i++ {
		t, _ := c.blocks[i].TargetInt()
		candWork.Sub(candWork, WorkOf(t))
	}
	for _, b := range newBlocks {
		t, _ := b.TargetInt()
		candWork.Add(candWork, WorkOf(t))
	}
	// Decentralized 51% guard #2: a candidate must always have more work, and for
	// deeper reorgs it must have *disproportionately* more (penalty).
	threshold := new(big.Int).Set(c.cumWork)
	if c.ReorgPenaltyPermille > 0 && depth > 1 {
		extra := new(big.Int).Mul(c.cumWork, big.NewInt(int64(depth*c.ReorgPenaltyPermille)))
		extra.Div(extra, big.NewInt(1000))
		threshold.Add(threshold, extra)
	}
	if candWork.Cmp(threshold) <= 0 {
		// Deterministic equal-work tie-break (depth-1, same height only): adopt a
		// competing equal-work tip iff its block hash is smaller, so a same-height
		// fork collapses deterministically in one round. MaxReorgDepth/checkpoint
		// guards (checked earlier) still bound this.
		candTip := newBlocks[len(newBlocks)-1]
		curTip := c.blocks[len(c.blocks)-1]
		tieBreakWin := depth == 1 &&
			candWork.Cmp(c.cumWork) == 0 &&
			candTip.Height == curTip.Height &&
			candTip.Hash() < curTip.Hash()
		if !tieBreakWin {
			return errors.New("candidate chain lacks sufficient work for its reorg depth")
		}
	}

	if depth == 0 {
		// FAST PATH — pure tip extension (the overwhelmingly common sync case).
		// Validate the new blocks against a COPY of the live state and apply them
		// incrementally (like AddBlock), then APPEND to disk. No genesis replay and
		// no full-file rewrite. The copy gives atomicity: if any block fails, the
		// live state is untouched.
		st := c.state.clone()
		tot := cloneTotals(c.totals)
		candidate := append(make([]*Block, 0, len(c.blocks)+len(newBlocks)), c.blocks...)
		for _, b := range newBlocks {
			if err := c.validateBlock(candidate, st, b); err != nil {
				return fmt.Errorf("candidate block %d: %w", b.Height, err)
			}
			candidate = append(candidate, b)
			applyBlockToState(st, b)
			applyBlockToTotals(tot, b)
		}
		c.blocks = candidate
		c.state = st
		c.totals = tot
		c.cumWork = candWork
		c.recomputeSupplyLocked()
		for _, b := range newBlocks {
			for _, tx := range b.Txs {
				delete(c.mempool, tx.ID())
			}
		}
		c.pruneMempoolLocked()
		var derr error
		for _, b := range newBlocks {
			if e := c.appendToDisk(b); e != nil {
				derr = e
			}
		}
		return derr
	}

	// REORG (depth>0, rare, bounded by MaxReorgDepth): rebuild candidate state from
	// the fork point. Correct and simple; deep reorgs are infrequent so the O(n)
	// replay here is acceptable (vs the complexity/risk of full undo logs). vmCache
	// stays content-keyed (vmFor), so it is not cleared.
	candidate := make([]*Block, startHeight, startHeight+uint64(len(newBlocks)))
	copy(candidate, c.blocks[:startHeight])
	st := State{}
	tot := map[string]*addrTotals{}
	for _, b := range candidate {
		applyBlockToState(st, b)
		applyBlockToTotals(tot, b)
	}
	for _, b := range newBlocks {
		if err := c.validateBlock(candidate, st, b); err != nil {
			return fmt.Errorf("candidate block %d: %w", b.Height, err)
		}
		candidate = append(candidate, b)
		applyBlockToState(st, b)
		applyBlockToTotals(tot, b)
	}
	c.blocks = candidate
	c.state = st
	c.totals = tot
	c.cumWork = candWork
	c.recomputeSupplyLocked()
	c.pruneMempoolLocked()
	return c.saveAll()
}

// ---------------------------------------------------------------- mempool

func (c *Chain) AddTx(t *Tx) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.IsCoinbase() {
		return errors.New("coinbase not allowed in mempool")
	}
	if _, ok := c.mempool[t.ID()]; ok {
		return errors.New("already in mempool")
	}
	// Replace-by-fee: a new tx at the same (sender, nonce) as a pending one
	// replaces it if it pays at least 10% more fee. This enables fee-bump (speed
	// up a stuck tx) and cancel (replace with a 0-value self-send). Non-consensus.
	if from, ferr := t.FromAddr(); ferr == nil {
		for id, m := range c.mempool {
			if m.Nonce != t.Nonce {
				continue
			}
			if mf, _ := m.FromAddr(); mf != from {
				continue
			}
			minFee := m.Fee + m.Fee/10
			if minFee <= m.Fee {
				minFee = m.Fee + 1
			}
			if t.Fee < minFee {
				return fmt.Errorf("replace-by-fee: fee %d must be >= %d (old fee + 10%%)", t.Fee, minFee)
			}
			delete(c.mempool, id) // drop the old, validate the new in its slot
			if err := c.validateMempoolTxLocked(t); err != nil {
				c.mempool[id] = m // restore on failure
				return err
			}
			c.mempool[t.ID()] = t
			return nil
		}
	}
	if len(c.mempool) > 10000 {
		return errors.New("mempool full")
	}
	if err := c.validateMempoolTxLocked(t); err != nil {
		return err
	}
	c.mempool[t.ID()] = t
	return nil
}

// validateMempoolTxLocked checks t against state plus queued txs from the same
// sender. A tx whose nonce runs ahead of the next executable nonce is accepted
// as "queued" (Ethereum-style) within MaxMempoolNonceGap / per-sender bounds
// rather than rejected - BuildTemplate only ever mines the contiguous run (it
// skips gaps and re-validates against live state), so a queued tx never affects
// block validity. This keeps the change non-consensus.
func (c *Chain) validateMempoolTxLocked(t *Tx) error {
	// A mempool tx targets the next block, whose height is len(blocks).
	if err := t.CheckSigAt(uint64(len(c.blocks))); err != nil {
		return err
	}
	if uint64(len(c.blocks)) >= MinFeeHeight {
		if m := minFeeFor(c.blocks); t.Fee < m {
			return fmt.Errorf("fee %d below minimum %d synapses", t.Fee, m)
		}
	}
	from, _ := t.FromAddr()
	acc := c.state.get(from)
	// Walk this sender's mempool txns to find: `nonce`, the next EXECUTABLE
	// nonce (contiguous frontier up from the account nonce); `spent`, the
	// balance committed by that executable chain; `held`, the sender's current
	// mempool count (for the per-sender cap).
	nonce := acc.Nonce
	spent := uint64(0)
	held := 0
	for _, m := range c.sortedMempoolLocked() {
		if mf, _ := m.FromAddr(); mf != from {
			continue
		}
		held++
		if m.Nonce == nonce {
			nonce++
			spent += m.Amount + m.Fee
		}
	}
	// A nonce below the account's is already spent on-chain - never minable.
	if t.Nonce < acc.Nonce {
		return fmt.Errorf("nonce too low: account at %d, got %d", acc.Nonce, t.Nonce)
	}
	// Hold a future-nonce tx instead of rejecting it, but bound how far ahead it
	// may sit and how many one sender may hold, so the queue can't be abused for
	// a memory-DoS.
	if t.Nonce > nonce+MaxMempoolNonceGap {
		return fmt.Errorf("nonce too far ahead: next executable %d, got %d (max gap %d)", nonce, t.Nonce, MaxMempoolNonceGap)
	}
	if held >= MaxMempoolTxnsPerSender {
		return fmt.Errorf("sender has too many queued txns (max %d)", MaxMempoolTxnsPerSender)
	}
	imm := immatureCoinbase(c.blocks, uint64(len(c.blocks)))
	avail := spendable(acc.Balance, imm[from])
	if t.Nonce == nonce {
		// Executable now: full running-balance check, exactly as before.
		if avail < spent+t.Amount+t.Fee {
			return errors.New("insufficient spendable balance (incl. pending / immature coinbase)")
		}
		return nil
	}
	// Queued (gap ahead): the exact running balance isn't known until the gap
	// fills, so only sanity-check the account could ever fund this tx alone.
	// BuildTemplate re-checks the real balance against live state before mining.
	if avail < t.Amount+t.Fee {
		return errors.New("insufficient spendable balance for queued tx")
	}
	return nil
}

func (c *Chain) sortedMempoolLocked() []*Tx {
	txs := make([]*Tx, 0, len(c.mempool))
	for _, t := range c.mempool {
		txs = append(txs, t)
	}
	sort.Slice(txs, func(i, j int) bool {
		if txs[i].Nonce != txs[j].Nonce {
			return txs[i].Nonce < txs[j].Nonce
		}
		if txs[i].Fee != txs[j].Fee {
			return txs[i].Fee > txs[j].Fee
		}
		return txs[i].ID() < txs[j].ID()
	})
	return txs
}

func (c *Chain) pruneMempoolLocked() {
	for id, t := range c.mempool {
		from, err := t.FromAddr()
		if err != nil {
			delete(c.mempool, id)
			continue
		}
		acc := c.state.get(from)
		if t.Nonce < acc.Nonce || acc.Balance < t.Amount+t.Fee {
			delete(c.mempool, id)
		}
	}
}

// MinedBlocks counts how many blocks were mined to addr (i.e. addr is the
// coinbase recipient). Used by the faucet to tier rewards by mining activity.
func (c *Chain) MinedBlocks(addr string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, b := range c.blocks {
		if len(b.Txs) > 0 && b.Txs[0].To == addr {
			n++
		}
	}
	return n
}

// AddrTotals scans the whole chain and returns lifetime totals for an address:
// total received, total mined (coinbase received), total sent (amount+fee), and
// the count of transactions touching the address. Unlike the 200-tx history
// window these are exact, so the explorer's "received/mined/sent" never undercount.
func (c *Chain) AddrTotals(addr string) (received, mined, sent uint64, txn int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if t := c.totals[addr]; t != nil {
		return t.Received, t.Mined, t.Sent, t.Txn
	}
	return 0, 0, 0, 0
}

// ------------------------------------------------------------- checkpoints

func (c *Chain) checkpointsFile() string { return filepath.Join(c.dir, "checkpoints.json") }

func (c *Chain) loadCheckpoints() {
	raw, err := os.ReadFile(c.checkpointsFile())
	if err != nil {
		return
	}
	var m map[uint64]string
	if json.Unmarshal(raw, &m) == nil && m != nil {
		c.Checkpoints = m
	}
}

func (c *Chain) saveCheckpoints() {
	raw, _ := json.Marshal(c.Checkpoints)
	_ = os.WriteFile(c.checkpointsFile(), raw, 0o644)
}

// ApplyCheckpoint records a verified authority checkpoint if it matches our own
// chain at that height. Returns false if we don't have that height yet or our
// block there conflicts (we are on a different chain).
func (c *Chain) ApplyCheckpoint(cp Checkpoint) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cp.Height == 0 || cp.Height >= uint64(len(c.blocks)) {
		return false
	}
	if c.blocks[cp.Height].Hash() != cp.Hash {
		return false
	}
	if c.Checkpoints[cp.Height] == cp.Hash {
		return true
	}
	c.Checkpoints[cp.Height] = cp.Hash
	c.saveCheckpoints()
	return true
}

// HighestCheckpointHeight returns the greatest checkpointed height (0 if none).
func (c *Chain) HighestCheckpointHeight() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var mx uint64
	for h := range c.Checkpoints {
		if h > mx {
			mx = h
		}
	}
	return mx
}

// --------------------------------------------------------------- building

// BuildTemplate assembles an unmined block paying to `coinbase`.
func (c *Chain) BuildTemplate(coinbase string) (*Block, error) {
	if !ValidAddr(coinbase) {
		return nil, errors.New("bad coinbase address")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	height := uint64(len(c.blocks))
	prev := c.blocks[len(c.blocks)-1]

	// Pick mempool txs greedily, respecting per-sender nonce order.
	st := State{}
	for k, v := range c.state {
		cp := *v
		st[k] = &cp
	}
	imm := immatureCoinbase(c.blocks, height)
	var minfee uint64
	if height >= MinFeeHeight {
		minfee = minFeeFor(c.blocks)
	}
	// Bitcoin-style fee market: repeatedly include the HIGHEST-fee tx that is the
	// next valid nonce for its sender (per-sender nonce order is mandatory). Under
	// congestion whoever pays more confirms sooner; with spare room everything
	// above the floor gets in. (Selection is policy, not consensus - any valid
	// block is accepted - so this needs no activation height.)
	bySender := map[string][]*Tx{}
	for _, t := range c.mempool {
		from, err := t.FromAddr()
		if err != nil {
			continue
		}
		bySender[from] = append(bySender[from], t)
	}
	for _, s := range bySender {
		sort.Slice(s, func(i, j int) bool { return s[i].Nonce < s[j].Nonce })
	}
	idx := map[string]int{} // consumed position in each sender's nonce-sorted list
	var picked []*Tx
	for len(picked) < MaxBlockTxs-1 {
		var best *Tx
		var bestFrom string
		for from, s := range bySender {
			i := idx[from]
			for i < len(s) && s[i].Nonce < st.get(from).Nonce { // skip stale nonces
				i++
			}
			idx[from] = i
			if i >= len(s) {
				continue
			}
			t := s[i]
			if t.Nonce != st.get(from).Nonce { // nonce gap - sender blocked for now
				continue
			}
			if t.Fee < minfee || validateTxAgainstState(st, t, imm, height) != nil {
				continue
			}
			if best == nil || t.Fee > best.Fee || (t.Fee == best.Fee && t.ID() < best.ID()) {
				best, bestFrom = t, from
			}
		}
		if best == nil {
			break
		}
		acc := st.get(bestFrom)
		acc.Balance -= best.Amount + best.Fee
		acc.Nonce++
		st.get(best.To).Balance += best.Amount
		picked = append(picked, best)
		idx[bestFrom]++
	}
	var fees uint64
	for _, t := range picked {
		fees += t.Fee
	}
	// Stamp this node's consensus version into the coinbase (free-form, unvalidated
	// Sig field) so the block votes toward readiness-gated upgrades. See upgrade.go.
	cb := &Tx{To: coinbase, Amount: BlockSubsidy(height) + fees, Nonce: height, Sig: coinbaseTag()}
	txs := append([]*Tx{cb}, picked...)

	now := uint64(time.Now().Unix())
	if mt := medianTime(c.blocks); now <= mt {
		now = mt + 1
	}
	// Don't hand out a template whose timestamp validateBlock would reject as too
	// far in the future (can happen if recent blocks pushed the median ahead).
	if now > uint64(time.Now().Unix())+MaxFutureDrift {
		return nil, errors.New("median time too far ahead; retry shortly")
	}
	b := &Block{
		Version:  1,
		Height:   height,
		Time:     now,
		PrevHash: prev.Hash(),
		TxRoot:   ComputeTxRoot(txs),
		Target:   TargetToHex(expectedTarget(c.blocks)),
		Nonce:    0,
		Txs:      txs,
	}
	return b, nil
}

// ----------------------------------------------------------------- views

func (c *Chain) Height() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return uint64(len(c.blocks)) - 1
}

func (c *Chain) Tip() *Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.blocks[len(c.blocks)-1]
}

// RecentCoinbaseShare returns the largest fraction of the last `window` blocks
// mined to one coinbase address, plus the count of distinct coinbase addresses.
// A share near/over 0.5 is a 51%-concentration signal. Read-only / observability.
func (c *Chain) RecentCoinbaseShare(window int) (float64, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := len(c.blocks)
	if window > n {
		window = n
	}
	if window <= 0 {
		return 0, 0
	}
	counts := map[string]int{}
	for i := n - window; i < n; i++ {
		b := c.blocks[i]
		if len(b.Txs) == 0 {
			continue
		}
		counts[b.Txs[0].To]++
	}
	top := 0
	for _, cnt := range counts {
		if cnt > top {
			top = cnt
		}
	}
	return float64(top) / float64(window), len(counts)
}

func (c *Chain) CumWork() *big.Int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return new(big.Int).Set(c.cumWork)
}

func (c *Chain) BlockAt(h uint64) *Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if h >= uint64(len(c.blocks)) {
		return nil
	}
	return c.blocks[h]
}

func (c *Chain) Blocks(from uint64, count int) []*Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if from >= uint64(len(c.blocks)) {
		return nil
	}
	end := from + uint64(count)
	if end > uint64(len(c.blocks)) {
		end = uint64(len(c.blocks))
	}
	out := make([]*Block, end-from)
	copy(out, c.blocks[from:end])
	return out
}

func (c *Chain) Account(addr string) Account {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if a := c.state[addr]; a != nil {
		return *a
	}
	return Account{}
}

// SpendableBalance is the matured balance: total minus coinbase rewards that
// have not yet reached CoinbaseMaturity (and so cannot be spent).
func (c *Chain) SpendableBalance(addr string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var bal uint64
	if a := c.state[addr]; a != nil {
		bal = a.Balance
	}
	imm := immatureCoinbase(c.blocks, uint64(len(c.blocks)))
	return spendable(bal, imm[addr])
}

func (c *Chain) MempoolTxs() []*Tx {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sortedMempoolLocked()
}

// MempoolLen returns the pending-tx count without sorting — cheap for status
// polls (which only need the number, not the sorted list).
func (c *Chain) MempoolLen() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.mempool)
}

func (c *Chain) Supply() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.supply
}

// recomputeSupplyLocked refreshes the cached total supply (Σ balances). Called
// under the write lock whenever state changes, so Supply() is O(1) on the hot
// /api/status path instead of an O(addresses) scan per request.
func (c *Chain) recomputeSupplyLocked() {
	var s uint64
	for _, a := range c.state {
		s += a.Balance
	}
	c.supply = s
}

// minFeeFor computes the cheap, self-adjusting fee floor (synapses) from how
// full the given blocks are: an idle network gives a tiny floor, and as blocks
// fill toward the cap it rises to ration space. Deterministic over the supplied
// prefix, so it doubles as the consensus minimum from MinFeeHeight on.
func minFeeFor(blocks []*Block) uint64 {
	const floor = 1000      // 0.00001 CRB while the network is idle
	const fullMult = 1000.0 // completely full blocks -> ~floor*1000 (still cheap)
	n := 20
	if len(blocks) < n {
		n = len(blocks)
	}
	capacity := n * (MaxBlockTxs - 1)
	if n <= 0 || capacity <= 0 {
		return floor
	}
	var txs int
	for i := len(blocks) - n; i < len(blocks); i++ {
		if t := len(blocks[i].Txs) - 1; t > 0 {
			txs += t
		}
	}
	fill := float64(txs) / float64(capacity) // 0..1
	if !feeMarketActiveAt(blocks, uint64(len(blocks))) {
		// legacy self-adjusting curve - kept byte-for-byte so nodes agree on
		// history and on new blocks until the fee market locks in (readiness-
		// gated at/after FeeMarketHeight; see core/upgrade.go).
		return uint64(float64(floor) * (1.0 + fullMult*fill*fill))
	}
	// Fee-market era: a tiny flat anti-spam floor only - no congestion scaling.
	// Congestion is handled Bitcoin-style by fee-priority block selection
	// (highest-fee txns first), so the floor never spikes and never strands
	// cheap txns or produces empty blocks while the mempool waits.
	return floor
}

// SuggestedFee returns a recommended fee (synapses) for timely confirmation.
// Below FeeMarketHeight it is the legacy self-adjusting floor. From
// FeeMarketHeight on, the consensus floor is flat, so congestion is reflected
// HERE instead (a wallet hint, not consensus): if more than one block's worth
// of txns are waiting, recommend just over the fee that would be cut from the
// next block; otherwise the next block clears everything, so recommend the floor.
func (c *Chain) SuggestedFee() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	floor := minFeeFor(c.blocks)
	if !feeMarketActiveAt(c.blocks, uint64(len(c.blocks))) {
		return floor
	}
	fees := make([]uint64, 0, len(c.mempool))
	for _, t := range c.mempool {
		fees = append(fees, t.Fee)
	}
	return suggestFee(fees, floor)
}

// suggestFee estimates the fee needed for next-block inclusion given the fees
// of the txns currently waiting and the hard floor. If one block fits the whole
// backlog, the floor confirms you; otherwise recommend just over the fee that
// would be cut from the next block. Pure (no locks/state) so it is unit-tested.
func suggestFee(memFees []uint64, floor uint64) uint64 {
	capacity := MaxBlockTxs - 1
	fees := make([]uint64, 0, len(memFees))
	for _, f := range memFees {
		if f >= floor {
			fees = append(fees, f)
		}
	}
	if len(fees) <= capacity {
		return floor // next block fits the whole mempool
	}
	sort.Slice(fees, func(i, j int) bool { return fees[i] > fees[j] }) // desc
	cut := fees[capacity-1]                                            // lowest fee still in the next block
	bump := cut / 8                                                    // ~12% over the cut so you clear it, not tie
	if bump == 0 {
		bump = 1
	}
	return cut + bump
}

// FeeFloor is the hard consensus minimum a tx must pay right now (synapses).
func (c *Chain) FeeFloor() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return minFeeFor(c.blocks)
}

// HistoryItem is a wallet-facing view of a confirmed transaction.
type HistoryItem struct {
	TxID   string `json:"txid"`
	Height uint64 `json:"height"`
	Time   uint64 `json:"time"`
	From   string `json:"from"` // "coinbase" for block rewards
	To     string `json:"to"`
	Amount uint64 `json:"amount"`
	Fee    uint64 `json:"fee"`
}

// History returns up to `limit` transactions touching addr, newest-first,
// skipping the first `offset` matches. offset enables stable "load more"
// paging without re-sending earlier pages; the total count is AddrTotals().Txn.
// The scan is over the in-RAM chain so even a deep offset is cheap.
func (c *Chain) History(addr string, limit, offset int) []HistoryItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if offset < 0 {
		offset = 0
	}
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
				out = append(out, HistoryItem{
					TxID: t.ID(), Height: b.Height, Time: b.Time,
					From: from, To: t.To, Amount: t.Amount, Fee: t.Fee,
				})
				if len(out) >= limit {
					break
				}
			}
		}
	}
	return out
}

// TxLocation is an explorer-facing view of a single transaction.
type TxLocation struct {
	Found     bool   `json:"found"`
	Pending   bool   `json:"pending"` // in mempool, not yet in a block
	TxID      string `json:"txid"`
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
	Time      uint64 `json:"time"`
	From      string `json:"from"` // "coinbase" for block rewards
	To        string `json:"to"`
	Amount    uint64 `json:"amount"`
	Fee       uint64 `json:"fee"`
	Nonce     uint64 `json:"nonce"`
	Coinbase  bool   `json:"coinbase"`
}

func txToLocation(t *Tx) TxLocation {
	from := "coinbase"
	if !t.IsCoinbase() {
		from, _ = t.FromAddr()
	}
	return TxLocation{
		Found: true, TxID: t.ID(), From: from, To: t.To,
		Amount: t.Amount, Fee: t.Fee, Nonce: t.Nonce, Coinbase: t.IsCoinbase(),
	}
}

// FindTx locates a transaction by id in the chain or mempool.
func (c *Chain) FindTx(id string) TxLocation {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.blocks) - 1; i >= 0; i-- {
		b := c.blocks[i]
		for _, t := range b.Txs {
			if t.ID() == id {
				loc := txToLocation(t)
				loc.Height = b.Height
				loc.BlockHash = b.Hash()
				loc.Time = b.Time
				return loc
			}
		}
	}
	if t, ok := c.mempool[id]; ok {
		loc := txToLocation(t)
		loc.Pending = true
		return loc
	}
	return TxLocation{Found: false, TxID: id}
}

// RichEntry is one row of the rich list.
type RichEntry struct {
	Address string `json:"address"`
	Balance uint64 `json:"balance"`
	Nonce   uint64 `json:"nonce"`
}

// RichList returns up to `n` addresses by balance, descending, starting at
// rank `offset` (0-based) so the explorer can page through every funded address.
func (c *Chain) RichList(n, offset int) []RichEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	list := make([]RichEntry, 0, len(c.state))
	for addr, a := range c.state {
		if a.Balance == 0 {
			continue
		}
		list = append(list, RichEntry{Address: addr, Balance: a.Balance, Nonce: a.Nonce})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Balance != list[j].Balance {
			return list[i].Balance > list[j].Balance
		}
		return list[i].Address < list[j].Address
	})
	if offset < 0 {
		offset = 0
	}
	if offset >= len(list) {
		return []RichEntry{}
	}
	list = list[offset:]
	if n > 0 && len(list) > n {
		list = list[:n]
	}
	return list
}

// AddrCount returns the number of funded (non-zero) addresses - the total for
// rich-list pagination.
func (c *Chain) AddrCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, a := range c.state {
		if a.Balance > 0 {
			n++
		}
	}
	return n
}

// BlockByHash returns a block by its id hash, or nil.
func (c *Chain) BlockByHash(hash string) *Block {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, b := range c.blocks {
		if b.Hash() == hash {
			return b
		}
	}
	return nil
}
