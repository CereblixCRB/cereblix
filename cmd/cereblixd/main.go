// cereblixd is the Cereblix full node daemon.
package main

import (
	"flag"
	"log"
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
		doUpdate = flag.Bool("update", false, "update to the latest released node (if newer) and exit")
		doDiag   = flag.Bool("diagnose", false, "print a self-diagnosis (environment, update state, recent boots) and exit")
		autoUpd  = flag.String("autoupdate", "", "persist auto-update preference: 'on' or 'off', then exit")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	if *doUpdate {
		runUpdateOnce()
		return
	}
	if *doDiag {
		runDiagnose(*datadir, *p2pAddr, *rpcAddr)
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

	chain, err := core.NewChain(*datadir)
	if err != nil {
		log.Fatalf("chain init: %v", err)
	}
	chain.MaxReorgDepth = *maxReorg
	chain.ReorgPenaltyPermille = *reorgPen
	log.Printf("cereblixd starting | height %d | tip %s | maxreorg %d", chain.Height(), chain.Tip().Hash()[:16], *maxReorg)

	var seeds []string
	for _, p := range strings.Split(*peers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			seeds = append(seeds, p)
		}
	}
	node.SetTrustedSubnets(*trustSub) // before New: addPeer() consults the trusted set
	n := node.New(chain, *datadir, *public, seeds)
	n.Version = nodeVersion
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
