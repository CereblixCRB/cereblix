package main

// node_embed.go runs an in-process Cereblix full node for FULL mode, wired exactly
// like cmd/cereblixd/main.go: core.OpenChain(datadir, bbolt) -> node.SetIsolated /
// SetTrustedSubnets / SetOwnIPs -> node.New(chain, datadir, public, seeds) ->
// n.Serve(p2p, rpc) in a goroutine. The node's RPC listens on loopback only, so the
// wallet's Lite/Custom RPC client can talk to it through the same code path.
//
// The node package exposes no graceful shutdown, so we start at most ONCE per
// process and keep it running for the app's lifetime; StopFullNode (in node_modes.go)
// simply stops *routing* wallet traffic to it. The start-once guard also prevents a
// "listen: address already in use" if the user toggles Full off and on again.

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"cereblix/core"
	"cereblix/node"
)

const (
	// embedRPCAddr is the loopback RPC the embedded node serves; the wallet's RPC
	// client targets embedEndpoint. embedP2PAddr is bound to loopback too so the
	// desktop wallet never becomes a publicly reachable P2P server.
	embedRPCAddr = "127.0.0.1:18751"
	embedP2PAddr = "127.0.0.1:18750"
	// embedEndpoint MUST include the /api prefix: the node mounts every RPC route
	// under /api/ (node.go RPCHandler), and the wallet's RPC client appends paths
	// like "/status" to this base — so Full mode needs ".../api" or it 404s.
	embedEndpoint = "http://127.0.0.1:18751/api"

	// walletNodeVersion is surfaced in the embedded node's /api/status. It is a
	// software label only and does not affect consensus signaling.
	walletNodeVersion = "wallet-embedded"
)

// embedSeeds is the seed the embedded node dials out to. node.New also adds the
// baked-in fallbackSeeds automatically (we are not isolated), so this is belt-and-
// suspenders for the primary seed.
var embedSeeds = []string{"http://seed.cereblix.com:18750"}

// embeddedNode owns the in-process node and guarantees single start.
type embeddedNode struct {
	mu      sync.Mutex
	started bool // Serve goroutine has been launched (never un-launched)
	chain   *core.Chain
	node    *node.Node
	dataDir string
}

func newEmbeddedNode() *embeddedNode { return &embeddedNode{} }

// nodeDataDir is <userhome>\.cereblix\node.
func nodeDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".cereblix", "node")
}

// start launches the embedded node once. Subsequent calls are no-ops (the node is
// already serving). Returns an error only if the chain fails to open.
func (e *embeddedNode) start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return nil
	}
	dir := nodeDataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	chain, err := core.OpenChain(dir, true /*bbolt*/, false /*importJSONL*/)
	if err != nil {
		return err
	}
	// Decentralized defaults, matching cereblixd's safe baseline.
	chain.MaxReorgDepth = 100

	node.SetIsolated(false)    // dial the public seeds (we want to sync the live chain)
	node.SetTrustedSubnets("") // no internal mesh exemption for a user wallet
	node.SetOwnIPs("")         // refuse self-dials; empty public URL = no advertised IP

	n := node.New(chain, dir, "" /*public: not advertised*/, embedSeeds)
	n.Version = walletNodeVersion

	e.chain = chain
	e.node = n
	e.dataDir = dir
	e.started = true

	go func() {
		log.Printf("embedded node: serving rpc %s, p2p %s (datadir %s)", embedRPCAddr, embedP2PAddr, dir)
		if err := n.Serve(embedP2PAddr, embedRPCAddr); err != nil {
			log.Printf("embedded node serve exited: %v", err)
		}
	}()
	return nil
}

// isStarted reports whether the Serve goroutine has been launched.
func (e *embeddedNode) isStarted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

// localHeight is the embedded chain's current height (0 before start).
func (e *embeddedNode) localHeight() uint64 {
	e.mu.Lock()
	c := e.chain
	e.mu.Unlock()
	if c == nil {
		return 0
	}
	return c.Height()
}
