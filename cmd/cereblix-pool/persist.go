package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// persist.go — single home for all crash / restart / failover state persistence.
//
// MONEY (never lost; survives a full node death) lives in Postgres, written through at each event
// and reconciled against on-chain truth: earned, inflight, payouts, the leader lease, and the block
// counter (meta 'blocks_found'). Double-pay is impossible across a failover because (a) only the
// leader-lease holder pays, (b) a sent payout's gross is held in `inflight` until the chain confirms
// it, (c) reconcile() re-derives owed/paid from the chain and adopts our still-pending mempool txs
// as inflight, and (d) all payouts share one wallet nonce so the network rejects any duplicate.
//
// RECOVERABLE state (the PPLNS payout window, the per-miner extranonce map, the rolling share-stats
// window) is snapshotted as one JSON blob to BOTH a local file (fast same-node restart) AND, in
// db-mode, a single Postgres row (survives a node death — Patroni replicates it to the standby).
// load() prefers Postgres, then disk. Losing this state only costs a brief rebuild, never money.
//
// One loop flushes everything; SIGTERM/SIGINT flushes once more before exit, so a PLANNED restart
// loses NOTHING and a hard crash loses at most one interval of the (rebuildable) recoverable state.
const (
	snapshotInterval = 15 * time.Second // disk + small Postgres snapshots (pplns, extranonce)
	statsDBEvery     = 4                 // the (larger) stats window goes to Postgres every 4th tick (~60s)
)

func snapshotLoop() {
	i := 0
	for {
		time.Sleep(snapshotInterval)
		i++
		saveShareEv(i%statsDBEvery == 0) // disk every tick; Postgres every ~60s (stats are loss-tolerant)
		savePPLNSSnapshot()
		saveExtranonce()
	}
}

// flushAll persists EVERYTHING right now (used by the graceful-shutdown handler).
func flushAll() {
	saveShareEv(true)
	savePPLNSSnapshot()
	saveExtranonce()
	saveFound()
}

func loadAllSnapshots() {
	loadShareEv()
	loadPPLNSSnapshot()
	loadExtranonce()
	loadFound()
}

// installShutdownFlush flushes ALL state on SIGTERM/SIGINT then exits 0, so a graceful stop
// (systemctl restart/stop) loses zero state. The periodic loop only bounds loss on a hard crash.
func installShutdownFlush() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-ch
		log.Printf("shutdown: got %v — flushing all snapshots before exit", s)
		flushAll()
		os.Exit(0)
	}()
}

// ----- blocks-found counter: a stat that must survive restart/failover (kept in the meta table) -----
// In db-mode st.Found is in memory only, so a cutover/restart used to reset the displayed block
// count to 0. We persist it to `meta` on each block + on shutdown and restore it on startup.

func saveFound() {
	if !dbMode {
		return // pool.json mode already persists Found in the state file
	}
	st.mu.Lock()
	n := st.Found
	st.mu.Unlock()
	if err := dbMetaSet("blocks_found", strconv.Itoa(n)); err != nil {
		log.Printf("blocks_found: db save: %v", err)
	}
}

func loadFound() {
	if !dbMode {
		return
	}
	v, err := dbMetaGet("blocks_found")
	if err != nil || v == "" {
		return
	}
	if n, e := strconv.Atoi(v); e == nil && n > 0 {
		st.mu.Lock()
		if n > st.Found {
			st.Found = n
		}
		st.mu.Unlock()
		log.Printf("blocks_found: restored %d from db", n)
	}
}
