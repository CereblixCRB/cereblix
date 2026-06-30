package main

// node_modes.go is the node-connectivity layer. The wallet talks to the chain in
// one of three modes:
//
//   lite   (default) - HTTP RPC to a built-in list of public node endpoints with
//                       simple round-robin failover (starts with cereblix.com/api).
//   custom           - HTTP RPC to a single user-supplied node URL.
//   full             - an in-process embedded node (node_embed.go) reached on
//                       loopback; the wallet trusts its own chain.
//
// The chosen mode plus the auto-lock timeout persist to
// <userhome>\.cereblix\desktop-settings.json. All RPC the App makes flows through
// get()/post() here so failover and mode selection live in one place.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// defaultLiteEndpoint is the seed of the built-in Lite endpoint list.
	defaultLiteEndpoint = "https://cereblix.com/api"

	defaultLockTimeoutMin = 15
)

// settingsFile is the on-disk settings document.
type settingsFile struct {
	NodeMode       string `json:"node_mode"`
	CustomURL      string `json:"custom_url,omitempty"`
	LockTimeoutMin int    `json:"lock_timeout_min"`
}

// NodeManager owns mode selection, the Lite endpoint rotation, persisted settings,
// and the embedded Full node. Safe for concurrent use.
type NodeManager struct {
	mu sync.Mutex

	rpc   *rpcClient
	embed *embeddedNode

	mode           string // "lite" | "full" | "custom"
	customURL      string
	lockTimeoutMin int

	liteEndpoints []string
	activeIdx     int // preferred index into liteEndpoints (last known-good)

	settingsPath string
}

func newNodeManager() *NodeManager {
	m := &NodeManager{
		rpc:            newRPCClient(),
		embed:          newEmbeddedNode(),
		mode:           "lite",
		lockTimeoutMin: defaultLockTimeoutMin,
		liteEndpoints:  []string{defaultLiteEndpoint},
		settingsPath:   settingsPath(),
	}
	m.loadSettings()
	return m
}

func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".cereblix", "desktop-settings.json")
}

// ------------------------------------------------------------- persistence

func (m *NodeManager) loadSettings() {
	raw, err := os.ReadFile(m.settingsPath)
	if err != nil {
		return // no settings yet; defaults apply
	}
	var sf settingsFile
	if json.Unmarshal(raw, &sf) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(sf.NodeMode)) {
	case "full", "custom", "lite":
		m.mode = strings.ToLower(strings.TrimSpace(sf.NodeMode))
	}
	if sf.CustomURL != "" {
		m.customURL = strings.TrimRight(sf.CustomURL, "/")
	}
	if sf.LockTimeoutMin > 0 {
		m.lockTimeoutMin = sf.LockTimeoutMin
	}
}

func (m *NodeManager) saveSettings() error {
	m.mu.Lock()
	sf := settingsFile{NodeMode: m.mode, CustomURL: m.customURL, LockTimeoutMin: m.lockTimeoutMin}
	path := m.settingsPath
	m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(&sf, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ------------------------------------------------------------- RPC routing

// basesOrdered returns the current mode and the ordered list of base URLs to try.
// For Lite the list rotates so the last known-good endpoint is tried first.
func (m *NodeManager) basesOrdered() (string, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.mode {
	case "full":
		return "full", []string{embedEndpoint}
	case "custom":
		if m.customURL == "" {
			return "custom", nil
		}
		return "custom", []string{m.customURL}
	default:
		n := len(m.liteEndpoints)
		if n == 0 {
			return "lite", nil
		}
		if m.activeIdx < 0 || m.activeIdx >= n {
			m.activeIdx = 0
		}
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, m.liteEndpoints[(m.activeIdx+i)%n])
		}
		return "lite", out
	}
}

// preferLite records base as the new preferred Lite endpoint after a success.
func (m *NodeManager) preferLite(base string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode != "lite" {
		return
	}
	for i, e := range m.liteEndpoints {
		if e == base {
			m.activeIdx = i
			return
		}
	}
}

// get performs a node GET through the active mode, failing over Lite endpoints on
// transport errors (but not on a logical node error).
func (m *NodeManager) get(path string, out any) error {
	mode, bases := m.basesOrdered()
	if len(bases) == 0 {
		return fmt.Errorf("no node endpoint configured for %s mode", mode)
	}
	var lastErr error
	for _, b := range bases {
		err := m.rpc.get(b, path, out)
		if err == nil {
			m.preferLite(b)
			return nil
		}
		lastErr = err
		if !isNetError(err) {
			return err // logical error: same on every endpoint, don't retry
		}
	}
	return lastErr
}

// post performs a node POST through the active mode with the same failover policy.
func (m *NodeManager) post(path string, body, out any) error {
	mode, bases := m.basesOrdered()
	if len(bases) == 0 {
		return fmt.Errorf("no node endpoint configured for %s mode", mode)
	}
	var lastErr error
	for _, b := range bases {
		err := m.rpc.post(b, path, body, out)
		if err == nil {
			m.preferLite(b)
			return nil
		}
		lastErr = err
		if !isNetError(err) {
			return err
		}
	}
	return lastErr
}

// activeEndpoint is the base URL currently shown to the user.
func (m *NodeManager) activeEndpoint() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeEndpointLocked()
}

func (m *NodeManager) activeEndpointLocked() string {
	switch m.mode {
	case "full":
		return embedEndpoint
	case "custom":
		return m.customURL
	default:
		n := len(m.liteEndpoints)
		if n == 0 {
			return ""
		}
		if m.activeIdx < 0 || m.activeIdx >= n {
			return m.liteEndpoints[0]
		}
		return m.liteEndpoints[m.activeIdx]
	}
}

// ------------------------------------------------------------- mode control

// setMode switches the active node mode and persists it. Full mode starts the
// embedded node; Custom requires a valid http(s) URL.
func (m *NodeManager) setMode(mode, customURL string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "lite":
		// nothing extra
	case "custom":
		u := strings.TrimRight(strings.TrimSpace(customURL), "/")
		if u == "" {
			return errors.New("custom mode requires a node URL")
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return errors.New("custom node URL must start with http:// or https://")
		}
		m.mu.Lock()
		m.customURL = u
		m.mu.Unlock()
	case "full":
		if err := m.embed.start(); err != nil {
			return fmt.Errorf("start embedded node: %w", err)
		}
	default:
		return fmt.Errorf("unknown node mode %q (use lite, full, or custom)", mode)
	}
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
	return m.saveSettings()
}

// startFull switches to Full mode (and starts the embedded node).
func (m *NodeManager) startFull() error { return m.setMode("full", "") }

// stopFull stops routing wallet traffic to the embedded node by switching back to
// Lite. The embedded node keeps running in the background for the process lifetime
// (the node package exposes no graceful shutdown), so a later StartFullNode reuses
// the already-synced chain instead of re-binding the listeners.
func (m *NodeManager) stopFull() error {
	m.mu.Lock()
	m.mode = "lite"
	m.mu.Unlock()
	return m.saveSettings()
}

// settings returns the current persisted settings view.
func (m *NodeManager) settings() SettingsResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SettingsResult{
		NodeMode:       m.mode,
		Endpoint:       m.activeEndpointLocked(),
		Endpoints:      append([]string(nil), m.liteEndpoints...),
		LockTimeoutMin: m.lockTimeoutMin,
	}
}

// setLockTimeout updates the auto-lock timeout (minutes) and persists it.
func (m *NodeManager) setLockTimeout(min int) error {
	if min < 0 {
		return errors.New("lock timeout cannot be negative")
	}
	m.mu.Lock()
	m.lockTimeoutMin = min
	m.mu.Unlock()
	return m.saveSettings()
}

// nodeInfo probes the active node for reachability, height and sync state.
func (m *NodeManager) nodeInfo() NodeInfoResult {
	m.mu.Lock()
	mode := m.mode
	m.mu.Unlock()
	res := NodeInfoResult{Mode: mode, Endpoint: m.activeEndpoint()}

	if mode == "full" {
		// Local height comes straight from the embedded chain (available before the
		// RPC listener is even up); the sync target comes from a public node.
		local := m.embed.localHeight()
		res.Height = local
		res.SyncHeight = local
		res.Reachable = m.embed.isStarted()
		var netSt struct {
			Height uint64 `json:"height"`
		}
		if m.rpc.get(defaultLiteEndpoint, "/status", &netSt) == nil && netSt.Height > local {
			res.SyncHeight = netSt.Height
			res.Syncing = true
		}
		return res
	}

	var st struct {
		Height uint64 `json:"height"`
	}
	err := m.get("/status", &st)
	res.Reachable = err == nil
	res.Height = st.Height
	res.SyncHeight = st.Height // a remote node does not expose its own sync target
	return res
}
