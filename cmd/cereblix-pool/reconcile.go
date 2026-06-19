package main

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"time"

	"cereblix/core"
)

// Chain-reconciled accounting. The ON-CHAIN payout history is the source of
// truth, not an optimistic in-memory counter. The pool persists only the
// cumulative `Earned` per miner; how much was actually paid is read back from
// the chain (`Delivered`). So after ANY node restart, reorg, mempool wipe or
// dropped payout, the books realign with reality on their own: whatever did not
// land on-chain is simply still-owed and gets re-sent. Coins are never silently
// lost, and this also auto-recovers payouts dropped before this change shipped.
//
// Double-pay is prevented by the in-flight set: a just-sent payout's gross is
// subtracted from a miner's payable balance until either its tx is seen on-chain
// (confirmed) or `confirmWindowBlocks` pass without it (dropped -> re-payable).

var chainFile string // path to the node's blocks.jsonl (co-located on the host)

// confirmWindowBlocks: a sent payout not seen on-chain within this many blocks
// is treated as dropped and becomes payable again. Long enough that a genuinely
// confirmed payout is always already counted in Delivered before we'd re-send.
const confirmWindowBlocks = 30

type inflight struct {
	Miner      string `json:"m"`
	Gross      uint64 `json:"g"`
	SentHeight uint64 `json:"h"`
}

// reconcile scans the chain, recomputes Delivered/Owed/Paid from on-chain truth,
// and expires in-flight payouts that have confirmed or been dropped.
func reconcile() {
	t0 := time.Now()
	f, err := os.Open(chainFile)
	if err != nil {
		log.Printf("pool: reconcile cannot read chain (%s): %v", chainFile, err)
		return
	}
	defer f.Close()
	delivered := map[string]uint64{}
	onchain := map[string]bool{}
	var height uint64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<25)
	for sc.Scan() {
		var b struct {
			Height uint64    `json:"height"`
			Txs    []core.Tx `json:"txs"`
		}
		if json.Unmarshal(sc.Bytes(), &b) != nil {
			continue
		}
		height = b.Height
		for i := range b.Txs {
			t := &b.Txs[i]
			if t.FromPub == "" {
				continue // coinbase
			}
			from, err := t.FromAddr()
			if err != nil || from != poolAddr {
				continue
			}
			delivered[t.To] += t.Amount + t.Fee // gross satisfied (net to miner + network fee)
			onchain[t.ID()] = true
		}
	}

	// Adopt any of OUR payout txns still pending in the mempool as in-flight, so
	// we never re-send a payout that's already broadcast but not yet confirmed
	// (e.g. ones the previous pool process sent right before a restart).
	var mp []core.Tx
	_ = nodeGet("/mempool", &mp)

	st.mu.Lock()
	defer st.mu.Unlock()
	if dbMode {
		// Refresh the in-memory caches from Postgres (the shared source of truth). This is
		// what lets a promoted standby take over the SAME accounting: it reads Earned +
		// inflight from the DB here. Runs every 60s (not the per-share hot path).
		if e, err := dbGetEarned(); err == nil {
			st.Earned = e
		} else {
			log.Printf("db: getEarned in reconcile: %v", err)
		}
		if fl, err := dbInflightList(); err == nil {
			st.InFlight = map[string]*inflight{}
			for _, r := range fl {
				st.InFlight[r.Txid] = &inflight{Miner: r.Addr, Gross: r.Gross, SentHeight: r.SentHeight}
			}
		} else {
			log.Printf("db: inflightList in reconcile: %v", err)
		}
	}
	for tid, fl := range st.InFlight {
		if onchain[tid] || height > fl.SentHeight+confirmWindowBlocks {
			delete(st.InFlight, tid) // confirmed (now in Delivered) or dropped (re-payable)
			if dbMode {
				_ = dbInflightDelete(tid)
			}
		}
	}
	for i := range mp {
		t := &mp[i]
		if t.FromPub == "" {
			continue
		}
		if from, err := t.FromAddr(); err == nil && from == poolAddr {
			if tid := t.ID(); !onchain[tid] {
				if _, ok := st.InFlight[tid]; !ok {
					st.InFlight[tid] = &inflight{Miner: t.To, Gross: t.Amount + t.Fee, SentHeight: height}
					if dbMode {
						_ = dbInflightAdd(tid, t.To, t.Amount+t.Fee, height)
					}
				}
			}
		}
	}
	paid := map[string]uint64{}
	owed := map[string]uint64{}
	for m, e := range st.Earned {
		d := delivered[m]
		paid[m] = d
		if e > d {
			owed[m] = e - d
		}
	}
	st.Paid = paid
	st.Owed = owed
	st.ChainHeight = height
	st.save()
	var owedTotal uint64
	for _, o := range owed {
		owedTotal += o
	}
	log.Printf("RECONCILE: height=%d earned_miners=%d owed_miners=%d owed_total=%s inflight=%d took=%dms",
		height, len(st.Earned), len(owed), crb(owedTotal), len(st.InFlight), time.Since(t0).Milliseconds())
}
