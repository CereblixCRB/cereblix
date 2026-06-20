package main

// Signed self-update for the Stratum bridge.
//
// The bridge is distributed to end users (their own solo :3334 against their own
// node) AND runs on our servers. Unlike the node it had NO update path, so a
// bridge bugfix (e.g. the half-open-connection fix) could not reach users at all.
// This adds the same authority-signed, sha256-verified, mirror-fallback update
// the node has — sharing core.UpgradeManifest and the one authority key — but via
// a SEPARATE manifest (stratum-upgrade.json) carrying the bridge's own version
// and binaries, so the node's and the bridge's release cadences stay independent.
//
// Safety, mirrored from the node:
//   - Every manifest is rejected unless it Verify()s against core.AuthorityPubKey,
//     so a hostile/MITM'd mirror cannot push a binary. The sha256 is inside the
//     signed payload and the download must match it.
//   - We only ever overwrite ourselves with a file whose URL is named
//     "cereblix-stratum…" (productGuard) — defense in depth so an operator who
//     accidentally publishes the NODE manifest at the stratum URL can't turn a
//     bridge into a node.
//   - A freshly-installed version is confirmed healthy (its listener comes up and
//     stays up); a crash-looping bad build is rolled back to the .old backup and
//     blacklisted so it is never reinstalled until a strictly-newer fix ships.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"cereblix/core"
)

// stratumVersion is this bridge binary's release version. The updater installs a
// strictly-newer one named in the authority-signed stratum manifest. Bump it on
// every bridge release and publish a matching stratum-upgrade.json.
const stratumVersion = "1.3"

// productGuard is the filename substring a stratum binary URL must contain before
// we will swap ourselves with it (see file header).
const productGuard = "cereblix-stratum"

// manifestURLs are tried in order. The bridge binaries live on the GitHub `xmrig`
// release (with the proxy + miner), so the stratum manifest sits there too, with
// our Cloudflare origin and the RU relay as GitHub-independent fallbacks (so a
// node behind a GitHub block in RU/CIS still updates). Every source is verified
// against the authority key, so an untrusted mirror cannot harm us.
var manifestURLs = []string{
	"https://github.com/CereblixCRB/cereblix/releases/download/xmrig/stratum-upgrade.json",
	"https://cereblix.com/stratum-upgrade.json",
	"https://ru.cereblix.com/stratum-upgrade.json",
}

func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH } // e.g. linux-amd64

// --- version comparison (pure) --------------------------------------------------

// verNewer reports whether version a > b ("1.10.0" > "1.9.0").
func verNewer(a, b string) bool {
	ap, bp := strings.Split(strings.TrimSpace(a), "."), strings.Split(strings.TrimSpace(b), ".")
	for i := 0; i < len(ap); i++ {
		var x, y int
		fmt.Sscanf(ap[i], "%d", &x)
		if i < len(bp) {
			fmt.Sscanf(bp[i], "%d", &y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// shouldInstall installs only something strictly newer than both what we run and
// any blacklisted (rolled-back) version, so a bad release is never re-downloaded
// in a loop yet the eventual fix (a higher version) installs automatically.
func shouldInstall(manifestVer, cur, blocked string) bool {
	if !verNewer(manifestVer, cur) {
		return false
	}
	if blocked != "" && !verNewer(manifestVer, blocked) {
		return false
	}
	return true
}

// --- manifest fetch + install ---------------------------------------------------

// manifestSources allows a comma-separated env override (tried first) for tests /
// private mirrors.
func manifestSources() []string {
	if v := strings.TrimSpace(os.Getenv("CEREBLIX_STRATUM_MANIFEST_URL")); v != "" {
		var extra []string
		for _, u := range strings.Split(v, ",") {
			if u = strings.TrimSpace(u); u != "" {
				extra = append(extra, u)
			}
		}
		return append(extra, manifestURLs...)
	}
	return manifestURLs
}

// fetchManifest returns the first authority-verified manifest among the sources.
func fetchManifest() (core.UpgradeManifest, bool) {
	cl := &http.Client{Timeout: 8 * time.Second}
	for _, u := range manifestSources() {
		resp, err := cl.Get(u)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		var m core.UpgradeManifest
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		if !m.Verify() { // reject anything not signed by the authority key
			log.Printf("auto-update: ignoring manifest from %s (bad/absent authority signature)", u)
			continue
		}
		return m, true
	}
	return core.UpgradeManifest{}, false
}

// selfUpdate downloads the binary named in the (already authority-verified)
// manifest, checks its sha256 and that it is a stratum binary, and atomically
// swaps it in keeping a .old backup.
func selfUpdate(bin core.UpgradeBinary) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	mirrors := bin.URLs
	if len(mirrors) == 0 && bin.URL != "" {
		mirrors = []string{bin.URL} // older single-mirror manifests
	}
	if len(mirrors) == 0 {
		return fmt.Errorf("manifest binary has no download URL")
	}
	var lastErr error
	for _, u := range mirrors {
		if !strings.Contains(u, productGuard) {
			lastErr = fmt.Errorf("refusing %s: not a %s binary", u, productGuard)
			log.Printf("auto-update: %v — trying next", lastErr)
			continue
		}
		data, err := downloadVerified(u, bin.SHA256)
		if err != nil {
			log.Printf("auto-update: mirror failed (%s): %v — trying next", u, err)
			lastErr = err
			continue
		}
		return swapBinary(exe, data)
	}
	return lastErr
}

// downloadVerified fetches a URL and returns its bytes only if they match the
// expected sha256 (and look like a real binary).
func downloadVerified(url, wantSHA string) ([]byte, error) {
	cl := &http.Client{Timeout: 240 * time.Second}
	resp, err := cl.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(data) < 1_000_000 {
		return nil, fmt.Errorf("file too small (%d bytes)", len(data))
	}
	if sum := hex.EncodeToString(sha256Sum(data)); !strings.EqualFold(sum, wantSHA) {
		return nil, fmt.Errorf("sha256 mismatch: got %s want %s", sum, wantSHA)
	}
	return data, nil
}

// swapBinary installs new bytes over the running executable, keeping a .old
// backup. Atomic on Unix; rename-aside on Windows (a running .exe can't be
// overwritten there).
func swapBinary(exe string, data []byte) error {
	os.Remove(exe + ".new")
	if err := os.WriteFile(exe+".new", data, 0o755); err != nil {
		return err
	}
	os.Remove(exe + ".old")
	if runtime.GOOS == "windows" {
		if err := os.Rename(exe, exe+".old"); err != nil {
			return err
		}
		if err := os.Rename(exe+".new", exe); err != nil {
			os.Rename(exe+".old", exe) // revert
			return err
		}
	} else {
		if cur, err := os.ReadFile(exe); err == nil {
			if err := os.WriteFile(exe+".old", cur, 0o755); err != nil {
				return fmt.Errorf("backup current binary: %w", err)
			}
		}
		if err := os.Rename(exe+".new", exe); err != nil {
			return err
		}
	}
	return nil
}

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// restartSelf relaunches the freshly-swapped binary. Under systemd (Restart=
// always) exiting is enough and avoids double instances; otherwise re-exec.
func restartSelf() {
	if os.Getenv("INVOCATION_ID") != "" {
		log.Print("auto-update: exiting for systemd to relaunch the new binary")
		os.Exit(0)
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if cmd.Start() == nil {
		os.Exit(0)
	}
}

// updateInterval is the gap between manifest checks (override for tests/ops). The
// bridge changes rarely, so it polls far less often than the node.
func updateInterval() time.Duration {
	if s := os.Getenv("CEREBLIX_STRATUM_UPDATE_INTERVAL_SECS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 6 * time.Hour
}

// autoUpdateLoop checks for a new signed manifest shortly after start, then
// periodically. Verification gates everything, so it is safe on every instance.
func autoUpdateLoop(enabled bool) {
	iv := updateInterval()
	init := iv
	if init > 15*time.Second {
		init = 15 * time.Second
	}
	time.Sleep(init)
	for {
		// Re-check the persistent opt-out each tick so `-autoupdate off` takes effect
		// without a restart.
		if enabled && !autoUpdateDisabled() {
			if m, ok := fetchManifest(); ok {
				applyManifest(m)
			}
		}
		time.Sleep(iv)
	}
}

// applyManifest installs a newer, non-blocked version if one is available.
func applyManifest(m core.UpgradeManifest) {
	blocked := blockedVersion()
	if !shouldInstall(m.Version, stratumVersion, blocked) {
		return
	}
	bin, ok := m.Binaries[platformKey()]
	if !ok {
		log.Printf("auto-update: manifest v%s has no binary for %s; update manually", m.Version, platformKey())
		return
	}
	log.Printf("auto-update: v%s available, downloading for %s ...", m.Version, platformKey())
	if err := selfUpdate(bin); err != nil {
		log.Printf("auto-update FAILED: %v (will retry next check)", err)
		return
	}
	log.Printf("auto-update: installed v%s, restarting", m.Version)
	markPending(m.Version) // arm the rollback guard for the next boot
	restartSelf()
}

// runUpdateOnce implements the -update flag: fetch, install if newer & not
// blocked, then exit.
func runUpdateOnce() {
	m, ok := fetchManifest()
	if !ok {
		fmt.Println("Could not fetch a valid stratum upgrade manifest (GitHub may be blocked; tried origin + relay too).")
		return
	}
	blocked := blockedVersion()
	if !shouldInstall(m.Version, stratumVersion, blocked) {
		if blocked != "" && !verNewer(m.Version, blocked) {
			fmt.Printf("Latest is v%s but it was rolled back as unhealthy earlier; waiting for a newer fix. Staying on v%s.\n", m.Version, stratumVersion)
		} else {
			fmt.Printf("Already up to date (v%s).\n", stratumVersion)
		}
		return
	}
	bin, ok := m.Binaries[platformKey()]
	if !ok {
		fmt.Printf("Manifest v%s has no binary for %s.\n", m.Version, platformKey())
		return
	}
	fmt.Printf("Updating v%s -> v%s ...\n", stratumVersion, m.Version)
	if err := selfUpdate(bin); err != nil {
		fmt.Println("Update failed:", err)
		return
	}
	markPending(m.Version)
	fmt.Println("✔ Updated to v" + m.Version + ". Restart cereblix-stratum to run it.")
}

// --- self-heal markers + boot guard (next to the executable) --------------------
//
// Simplified vs the node (a bridge is a stateless protocol adapter — no chain or
// data to corrupt; its only environment dependency is a free listen port):
//   .old        previous binary (rollback target)
//   .pending    version just installed, being confirmed healthy
//   .bootn      consecutive unconfirmed boots of .pending
//   .badversion version proven bad (rolled back from); skipped until a newer fix
//   .noupdate   operator opt-out (persisted; survives binary swaps)

const maxBadBoots = 3 // unconfirmed boots of a pending version before rollback

func exePath() string         { p, _ := os.Executable(); return p }
func mk(suffix string) string { return exePath() + suffix }
func exists(p string) bool    { _, err := os.Stat(p); return err == nil }
func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}
func readBootn() int { n, _ := strconv.Atoi(readTrim(mk(".bootn"))); return n }

// markPending records a freshly-installed version and resets the boot counter.
func markPending(version string) {
	os.WriteFile(mk(".pending"), []byte(version), 0o644)
	os.WriteFile(mk(".bootn"), []byte("0"), 0o644)
}

// blockedVersion is the version we must NOT (re)install. shouldInstall skips <= it.
func blockedVersion() string { return readTrim(mk(".badversion")) }

// autoUpdateDisabled reports the operator's persistent opt-out (`.noupdate`).
func autoUpdateDisabled() bool { return exists(mk(".noupdate")) }

// setAutoUpdate persists the operator's choice and prints what changed.
func setAutoUpdate(on bool) {
	if on {
		os.Remove(mk(".noupdate"))
		fmt.Println("Auto-update ENABLED: this bridge will install authority-signed releases automatically (verified + rollback).")
	} else {
		os.WriteFile(mk(".noupdate"), []byte("disabled by operator"), 0o644)
		fmt.Println("Auto-update DISABLED and persisted. Re-enable any time with:  cereblix-stratum -autoupdate on")
		fmt.Println("(Update manually with `cereblix-stratum -update`.)")
	}
}

// confirmWindow is how long a freshly-booted binary has to come up before the boot
// is treated as failed (override for tests).
func confirmWindow() time.Duration {
	if s := os.Getenv("CEREBLIX_STRATUM_CONFIRM_SECS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}

// canBind reports whether the listen address is free — i.e. a failed boot is the
// environment (port in use / another instance), not a bad binary, so we must not
// blame the pending version for it.
func canBind(listen string) bool {
	la := listen
	if strings.HasPrefix(la, ":") {
		la = "0.0.0.0" + la
	}
	ln, err := net.Listen("tcp", la)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// listenerHealthy reports whether the bridge's own Stratum port accepts a
// connection — the bridge's notion of "up" (analogous to the node's RPC probe).
func listenerHealthy(listen string) bool {
	host := listen
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	} else if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1" + strings.TrimPrefix(host, "0.0.0.0")
	}
	c, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// waitHealthy polls until the listener is up or the confirm window expires.
func waitHealthy(listen string) bool {
	deadline := time.Now().Add(confirmWindow())
	for time.Now().Before(deadline) {
		if listenerHealthy(listen) {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return listenerHealthy(listen)
}

// bootDecision is what a boot should do about a pending self-update. Kept as a
// pure decision (decideBoot) so the rollback/confirm logic is unit-tested without
// running a real crash loop.
type bootDecision int

const (
	bootClean    bootDecision = iota // nothing pending; tidy stale markers
	bootEnvBusy                      // can't bind => environment fault, not the binary
	bootWatch                        // confirm a freshly-installed version in the background
	bootRollback                     // pending crash-looped in a healthy env; revert to .old
	bootGiveUp                       // pending crash-looped but there is no backup to revert to
)

// decideBoot is pure: given the markers + this boot's attempt count, decide.
func decideBoot(hasPending, envOK, hasOld bool, attempts int) bootDecision {
	if !hasPending {
		return bootClean
	}
	if !envOK {
		return bootEnvBusy
	}
	if attempts > maxBadBoots {
		if hasOld {
			return bootRollback
		}
		return bootGiveUp
	}
	return bootWatch
}

// bootGuard runs once at startup, BEFORE main binds the listener. It confirms a
// freshly-installed version healthy, or rolls a crash-looping one back to .old and
// blacklists it. On rollback it restarts and does not return. (Like the node, the
// rollback rename is reliable on Unix/macOS — where the servers and most users run
// — and best-effort on Windows, where a running .exe cannot be overwritten.)
func bootGuard(listen string) {
	hasPending := exists(mk(".pending"))
	pending := readTrim(mk(".pending"))
	attempts := readBootn() + 1
	envOK := hasPending && canBind(listen) // only probe the port when a pending boot needs it

	switch decideBoot(hasPending, envOK, exists(mk(".old")), attempts) {
	case bootClean:
		os.Remove(mk(".old"))
		os.Remove(mk(".bootn"))

	case bootEnvBusy:
		// The real bind below will fail loudly with the true cause (or succeed if it
		// was transient); don't count this boot against the pending binary.
		log.Printf("auto-update: cannot bind %s at boot (port in use / another instance?) — NOT counting against pending v%s", listen, pending)

	case bootRollback:
		log.Printf("auto-update ROLLBACK: v%s failed to come up %dx in a healthy environment; reverting to the previous binary and blacklisting v%s", pending, attempts-1, pending)
		if pending != "" {
			os.WriteFile(mk(".badversion"), []byte(pending), 0o644)
		}
		os.Remove(mk(".pending"))
		os.Remove(mk(".bootn"))
		if os.Rename(mk(".old"), exePath()) == nil {
			restartSelf()
			return
		}
		log.Printf("auto-update: rollback rename failed; continuing on v%s", pending)

	case bootGiveUp:
		log.Printf("⚠ auto-update: v%s did not come up and there is NO backup to roll back to. Continuing on it.", pending)
		os.Remove(mk(".pending"))
		os.Remove(mk(".bootn"))

	case bootWatch:
		// Record this attempt, then confirm in the background once main has bound the
		// listener.
		os.WriteFile(mk(".bootn"), []byte(strconv.Itoa(attempts)), 0o644)
		go func() {
			if waitHealthy(listen) {
				os.Remove(mk(".pending"))
				os.Remove(mk(".bootn"))
				os.Remove(mk(".old"))
				os.Remove(mk(".badversion")) // a newer healthy version supersedes any blacklist
				log.Printf("auto-update: v%s confirmed healthy, committed", pending)
			} else {
				log.Printf("auto-update: v%s did not come up within %s; next boot counts toward rollback (%d/%d)", pending, confirmWindow(), attempts, maxBadBoots)
			}
		}()
	}
}

// printVersion implements -version.
func printVersion() { fmt.Printf("cereblix-stratum v%s\n", stratumVersion) }
