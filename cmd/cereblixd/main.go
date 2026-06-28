// cereblixd is the Cereblix full node daemon.
package main

import (
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on DefaultServeMux; only served if CEREBLIX_PPROF is set
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cereblix/core"
	"cereblix/node"
)

func main() {
	var (
		datadir  = flag.String("datadir", "cereblix-data", "data directory")
		p2pAddr  = flag.String("p2p", ":18750", "p2p listen address")
		rpcAddr  = flag.String("rpc", "127.0.0.1:18751", "rpc listen address")
		peers    = flag.String("peers", "http://seed.cereblix.com:18750", "comma-separated seed peer URLs")
		public   = flag.String("public", "", "publicly reachable URL of this node (advertised to peers)")
		trustSub = flag.String("trustedsubnet", "", "comma-separated CIDR(s) exempt from the P2P SSRF guard for an internal mesh, e.g. 10.10.0.0/24 (default: none = full guard)")
		mine     = flag.Bool("mine", false, "enable built-in miner")
		threads  = flag.Int("threads", 2, "miner threads")
		coinbase = flag.String("coinbase", "", "address that receives block rewards")
		maxReorg = flag.Uint64("maxreorg", 100, "reject reorgs deeper than N blocks (0 = unlimited); decentralized 51% guard")
		reorgPen = flag.Uint64("reorg-penalty", 0, "extra work permille per reorg-depth block required (0 = off)")
		noUpdate = flag.Bool("noupdate", false, "disable automatic node self-update for this run (one-off; see -autoupdate to persist)")
		stallRst = flag.Bool("stall-restart", false, "let the liveness watchdog exit(1) for a supervisor restart if the node stays behind+stuck (off by default; needs Restart= in the unit)")
		isolated = flag.Bool("isolated", false, "vacuum/testnet mode: do NOT dial the baked-in public seeds, peer ONLY with -peers (no contact with the live network)")
		doUpdate = flag.Bool("update", false, "update to the latest released node (if newer) and exit")
		doDiag   = flag.Bool("diagnose", false, "print a self-diagnosis (environment, update state, recent boots) and exit")
		autoUpd  = flag.String("autoupdate", "", "persist auto-update preference: 'on' or 'off', then exit")
		store    = flag.String("store", "jsonl", "chain store backend: jsonl (default) | bbolt (2.3.0; re-syncs from peers on first start). NOTE: cereblix-pool reads blocks.jsonl directly, so pool nodes must stay jsonl until the pool reads via the node API")
		expJSONL = flag.Bool("export-jsonl", false, "export the bbolt store to blocks.jsonl (rollback to jsonl), then exit")
		impJSONL = flag.Bool("import-jsonl", false, "on first bbolt start, import an existing blocks.jsonl instead of re-syncing from peers (faster; for a trusted local chain)")
		doCompact = flag.Bool("compact", false, "compact the bbolt chain.db (reclaim free pages into a fresh file), then exit; run with the node STOPPED (keeps chain.db.precompact as rollback)")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	// Optional profiling endpoint, OFF unless CEREBLIX_PPROF is set (intended for a
	// loopback address like 127.0.0.1:6060). Never exposed by default.
	if addr := os.Getenv("CEREBLIX_PPROF"); addr != "" {
		go func() { log.Printf("pprof listener exited: %v", http.ListenAndServe(addr, nil)) }()
		log.Printf("pprof: serving /debug/pprof on %s", addr)
	}

	if *doUpdate {
		runUpdateOnce()
		return
	}
	if *doDiag {
		runDiagnose(*datadir, *p2pAddr, *rpcAddr)
		return
	}
	if *expJSONL {
		n, err := core.ExportBoltToJSONL(*datadir)
		if err != nil {
			log.Fatalf("export-jsonl: %v", err)
		}
		log.Printf("export-jsonl: wrote %d blocks to blocks.jsonl", n)
		return
	}
	if *doCompact {
		old, neu, err := core.CompactStore(*datadir)
		if err != nil {
			log.Fatalf("compact: %v", err)
		}
		pct := 0.0
		if old > 0 {
			pct = 100 * (1 - float64(neu)/float64(old))
		}
		log.Printf("compact: chain.db %d -> %d bytes (%.1f%% smaller); original kept as chain.db.precompact", old, neu, pct)
		return
	}
	if *autoUpd != "" {
		switch strings.ToLower(strings.TrimSpace(*autoUpd)) {
		case "on", "true", "1", "enable":
			setAutoUpdate(true)
		case "off", "false", "0", "disable":
			setAutoUpdate(false)
		default:
			log.Println("usage: cereblixd -autoupdate on|off")
		}
		return
	}
	bootGuard(*datadir, *p2pAddr, *rpcAddr)

	useBolt := !strings.EqualFold(strings.TrimSpace(*store), "jsonl") // default bbolt; only explicit "jsonl" opts out
	chain, err := core.OpenChain(*datadir, useBolt, *impJSONL)
	if err != nil {
		log.Fatalf("chain init: %v", err)
	}
	log.Printf("chain store: %s (requested %q)", map[bool]string{true: "bbolt", false: "jsonl"}[chain.UsingBolt()], *store)
	chain.MaxReorgDepth = *maxReorg
	chain.ReorgPenaltyPermille = *reorgPen
	log.Printf("cereblixd starting | height %d | tip %s | maxreorg %d", chain.Height(), chain.Tip().Hash()[:16], *maxReorg)

	var seeds []string
	for _, p := range strings.Split(*peers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			seeds = append(seeds, p)
		}
	}
	node.SetIsolated(*isolated)       // before New: gate the baked-in fallback seeds
	node.SetTrustedSubnets(*trustSub) // before New: addPeer() consults the trusted set
	if !*isolated {
		node.SetOwnIPs(*public) // before New: dialer refuses our own IPs (kills seed round-robin self-dials). Skipped in -isolated so a vacuum testnet can peer over loopback.
	}
	n := node.New(chain, *datadir, *public, seeds)
	n.Version = nodeVersion
	n.StallRestart = *stallRst
	log.Printf("node software v%s (consensus v%d)", nodeVersion, core.NodeConsensusVersion)
	go autoUpdateLoop(n, !*noUpdate)

	if *mine {
		if !core.ValidAddr(*coinbase) {
			log.Println("error: -mine requires a valid -coinbase address (create one with cereblix-wallet new)")
			os.Exit(1)
		}
		n.Mine(*threads, *coinbase)
	}

	// Persist the mempool periodically and on graceful shutdown so pending txns
	// (including pool payouts) survive a restart instead of being silently dropped.
	go func() {
		for range time.Tick(10 * time.Second) {
			chain.SaveMempool()
		}
	}()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		_ = chain.SaveMempool()
		log.Print("mempool persisted; shutting down")
		os.Exit(0)
	}()

	log.Fatal(n.Serve(*p2pAddr, *rpcAddr))
}
