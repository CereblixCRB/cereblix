package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cereblix/core"
)

// Self-healing auto-update guard.
//
// Goal: in EVERY situation the node ends up running a healthy binary - never
// stuck, never thrashing - AND it self-diagnoses WHY a boot failed, so a genuinely
// bad release is told apart from a broken environment.
//
// The core insight: a rollback is also a DIAGNOSIS. If reverting to the previous
// binary makes the node healthy, the new binary really was bad (blacklist it).
// If the previous binary ALSO fails to become healthy, the fault is the
// environment/data, NOT the binary - so we must NOT blacklist the new version;
// we pause auto-update and shout for a human instead. This makes a wrongly-
// suspected good version recoverable.
//
// Markers (next to the executable):
//   .old        previous binary (rollback target)
//   .pending    version just installed, being confirmed
//   .bootn      consecutive unconfirmed boots of .pending
//   .suspect    version we rolled back FROM, not yet proven bad (tentative)
//   .badversion version proven bad (confirmed blacklist; skip until a newer fix)
//   .envhalt    auto-update paused: an environment fault a rollback couldn't fix
//   .bad        the rolled-out binary set aside on rollback (for inspection)
// Diagnostics trail: <datadir>/bootlog.jsonl

const (
	maxBadBoots      = 3 // unconfirmed boots of a pending version before rollback
	confirmPollEvery = 3 * time.Second
)

func exePath() string         { p, _ := os.Executable(); return p }
func mk(suffix string) string { return exePath() + suffix }
func exists(p string) bool    { _, err := os.Stat(p); return err == nil }
func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// --- environment self-diagnosis -------------------------------------------------

type diagnosis struct {
	OK       bool
	Problems []string
}

func (d diagnosis) summary() string {
	if d.OK {
		return "environment OK"
	}
	return strings.Join(d.Problems, "; ")
}

// selfDiagnose checks the things that make a GOOD binary fail to start, so those
// failures are not mistaken for a bad release: a writable datadir and free ports.
func selfDiagnose(datadir, p2p, rpc string) diagnosis {
	var probs []string
	if err := os.MkdirAll(datadir, 0o755); err != nil {
		probs = append(probs, "datadir not creatable ("+datadir+"): "+err.Error())
	} else {
		t := datadir + "/.writetest"
		if err := os.WriteFile(t, []byte("ok"), 0o644); err != nil {
			probs = append(probs, "datadir not writable ("+datadir+"): "+err.Error())
		} else {
			os.Remove(t)
		}
	}
	for _, addr := range []string{p2p, rpc} {
		la := addr
		if strings.HasPrefix(la, ":") {
			la = "0.0.0.0" + la
		}
		ln, err := net.Listen("tcp", la)
		if err != nil {
			probs = append(probs, "cannot bind "+addr+" (port in use / another instance?): "+err.Error())
		} else {
			ln.Close()
		}
	}
	return diagnosis{OK: len(probs) == 0, Problems: probs}
}

// --- boot decision (pure, unit-tested) ------------------------------------------

type bootDecision int

const (
	bootNormal           bootDecision = iota // clean boot, nothing pending
	bootWatch                                // confirm a freshly-installed version
	bootConfirmRollback                      // we are the rollback target; confirm to blacklist the suspect
	bootRollback                             // pending failed in a healthy env; revert to .old
	bootGiveUp                               // pending failed but no backup to revert to
	bootEnvProblem                           // preflight failed: environment is broken, not the binary
	bootEnvFaultConfirmed                    // the rollback target ALSO keeps failing -> env/data fault, not the binary
)

type bootState struct {
	EnvOK           bool
	HasPending      bool
	Attempts        int // unconfirmed boots of the pending version (this boot included)
	HasOld          bool
	HasSuspect      bool
	ConfirmAttempts int // unconfirmed boots of the rollback target while a suspect is pending
}

func decideBoot(s bootState) bootDecision {
	if !s.EnvOK {
		return bootEnvProblem
	}
	if s.HasPending {
		if s.Attempts >= maxBadBoots {
			if s.HasOld {
				return bootRollback
			}
			return bootGiveUp
		}
		return bootWatch
	}
	if s.HasSuspect {
		// We rolled back; this is the previous binary booting. If IT also fails to
		// confirm healthy maxBadBoots times, the fault is the environment/data, not
		// the suspected binary - so we must NOT blacklist it.
		if s.ConfirmAttempts >= maxBadBoots {
			return bootEnvFaultConfirmed
		}
		return bootConfirmRollback
	}
	return bootNormal
}

// --- markers & helpers ----------------------------------------------------------

// markPending records a freshly-installed version and resets the boot counter.
func markPending(version string) {
	os.WriteFile(mk(".pending"), []byte(version), 0o644)
	os.WriteFile(mk(".bootn"), []byte("0"), 0o644)
}

func readBootn() int    { n, _ := strconv.Atoi(readTrim(mk(".bootn"))); return n }
func readConfirmn() int { n, _ := strconv.Atoi(readTrim(mk(".confirmn"))); return n }

// clearSuspectAsEnvFault concludes that a rollback did NOT help, so the suspected
// version was not actually the problem: un-blacklist it and pause auto-update
// until the environment/data is fixed (a later healthy boot lifts the pause).
func clearSuspectAsEnvFault(datadir, suspect, why string) {
	os.Remove(mk(".suspect")) // do NOT blacklist - never proven bad
	os.Remove(mk(".confirmn"))
	os.WriteFile(mk(".envhalt"), []byte("environment/data fault: "+why), 0o644)
	bootlog(datadir, suspect, "env-fault", why+"; not blacklisting")
	log.Printf("⚠ auto-update: the PREVIOUS binary also failed to become healthy (%s). The fault is NOT the binary (likely data/config/environment). NOT blacklisting v%s. Auto-update paused — run `cereblixd -diagnose` and fix manually.", why, suspect)
}

// blockedVersion is the version we must NOT (re)install: the confirmed-bad one or
// the still-tentative suspect (the higher of the two). shouldInstall skips <= it.
func blockedVersion() string {
	bad, sus := readTrim(mk(".badversion")), readTrim(mk(".suspect"))
	switch {
	case bad == "":
		return sus
	case sus == "":
		return bad
	case verNewer(sus, bad):
		return sus
	default:
		return bad
	}
}

func envHalted() bool { return exists(mk(".envhalt")) }

// autoUpdateDisabled reports the operator's persistent opt-out (a `.noupdate`
// marker next to the binary, set via `cereblixd -autoupdate off`). It survives
// restarts and binary swaps - no need to edit the service unit or pass flags.
func autoUpdateDisabled() bool { return exists(mk(".noupdate")) }

// setAutoUpdate persists the operator's choice and prints what changed.
func setAutoUpdate(on bool) {
	if on {
		os.Remove(mk(".noupdate"))
		fmt.Println("Auto-update ENABLED: this node will install authority-signed releases automatically (with verification + rollback).")
	} else {
		os.WriteFile(mk(".noupdate"), []byte("disabled by operator"), 0o644)
		fmt.Println("Auto-update DISABLED and persisted. Re-enable any time with:  cereblixd -autoupdate on")
		fmt.Println("(The node still fetches the signed manifest and WARNS about required network upgrades — it just won't install them. You'd then update manually with `cereblixd -update`.)")
	}
}

// confirmWindow is how long a freshly-booted binary has to become healthy before
// the boot is treated as failed (override for tests / very large chains).
func confirmWindow() time.Duration {
	if s := os.Getenv("CEREBLIX_CONFIRM_SECS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 90 * time.Second
}

// rpcHealthy reports whether the node serves /api/status (chain loaded + RPC up).
func rpcHealthy(rpc string) bool {
	host := rpc
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	} else if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1" + strings.TrimPrefix(host, "0.0.0.0")
	}
	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get("http://" + host + "/api/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// waitHealthy polls until the node is healthy or the confirm window expires.
func waitHealthy(rpc string) bool {
	deadline := time.Now().Add(confirmWindow())
	for time.Now().Before(deadline) {
		if rpcHealthy(rpc) {
			return true
		}
		time.Sleep(confirmPollEvery)
	}
	return rpcHealthy(rpc)
}

func bootlog(datadir, version, event, detail string) {
	rec := map[string]string{
		"t": time.Now().UTC().Format(time.RFC3339), "v": version, "event": event, "detail": detail,
	}
	b, _ := json.Marshal(rec)
	if f, err := os.OpenFile(datadir+"/bootlog.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
}

// --- boot guard -----------------------------------------------------------------

// bootGuard runs once at startup, before the node starts. It diagnoses the
// environment, decides confirm/rollback/proceed, performs any binary swap, and
// (when proceeding) schedules the background health confirmation. On rollback it
// restarts the process and does not return.
func bootGuard(datadir, p2p, rpc string) {
	diag := selfDiagnose(datadir, p2p, rpc)
	st := bootState{
		EnvOK:           diag.OK,
		HasPending:      exists(mk(".pending")),
		Attempts:        readBootn() + 1,
		HasOld:          exists(mk(".old")),
		HasSuspect:      exists(mk(".suspect")),
		ConfirmAttempts: readConfirmn() + 1,
	}

	switch decideBoot(st) {
	case bootEnvProblem:
		bootlog(datadir, nodeVersion, "env-problem", diag.summary())
		log.Printf("⚠ STARTUP ENVIRONMENT PROBLEM — %s", diag.summary())
		log.Print("  Not a binary fault: not rolling back or blacklisting. Fix it and the node recovers on restart. Run `cereblixd -diagnose`.")
		if st.HasPending || st.HasSuspect {
			// don't thrash binaries over an environment fault, and don't count this
			// boot against the pending binary.
			os.WriteFile(mk(".envhalt"), []byte(diag.summary()), 0o644)
		}
		return // proceed to start; Serve fails loudly on the real cause

	case bootRollback:
		failed := readTrim(mk(".pending"))
		bootlog(datadir, failed, "rollback", fmt.Sprintf("unconfirmed %dx in a healthy env", st.Attempts))
		log.Printf("auto-update ROLLBACK: v%s failed to become healthy %dx in a healthy environment; reverting to the previous binary", failed, st.Attempts)
		if failed != "" {
			os.WriteFile(mk(".suspect"), []byte(failed), 0o644) // tentative until the old binary proves healthy
		}
		os.WriteFile(mk(".confirmn"), []byte("0"), 0o644) // fresh confirm cycle for the rollback target
		os.Rename(exePath(), mk(".bad"))
		if os.Rename(mk(".old"), exePath()) == nil {
			os.Remove(mk(".pending"))
			os.Remove(mk(".bootn"))
			restartSelf()
			return
		}
		os.Rename(mk(".bad"), exePath()) // revert the revert
		os.Remove(mk(".pending"))
		os.Remove(mk(".bootn"))
		return

	case bootGiveUp:
		failed := readTrim(mk(".pending"))
		bootlog(datadir, failed, "giveup", "no backup to roll back to")
		log.Printf("⚠ auto-update: v%s did not become healthy and there is NO backup to roll back to. Continuing on it; run `cereblixd -diagnose`.", failed)
		os.Remove(mk(".pending"))
		os.Remove(mk(".bootn"))
		return

	case bootWatch:
		os.WriteFile(mk(".bootn"), []byte(strconv.Itoa(st.Attempts)), 0o644)
		ver := readTrim(mk(".pending"))
		bootlog(datadir, ver, "watch", fmt.Sprintf("attempt %d/%d", st.Attempts, maxBadBoots))
		go func() {
			if waitHealthy(rpc) {
				os.Remove(mk(".pending"))
				os.Remove(mk(".bootn"))
				os.Remove(mk(".confirmn"))
				os.Remove(mk(".old"))
				os.Remove(mk(".bad"))
				os.Remove(mk(".badversion")) // a newer healthy version supersedes any blacklist
				os.Remove(mk(".suspect"))
				os.Remove(mk(".envhalt"))
				bootlog(datadir, ver, "committed", "healthy")
				log.Printf("auto-update: v%s confirmed healthy, committed", ver)
			} else {
				bootlog(datadir, ver, "unhealthy", "no RPC within confirm window")
				log.Printf("auto-update: v%s did not become healthy within %s; next boot counts toward rollback", ver, confirmWindow())
			}
		}()

	case bootConfirmRollback:
		suspect := readTrim(mk(".suspect"))
		os.WriteFile(mk(".confirmn"), []byte(strconv.Itoa(st.ConfirmAttempts)), 0o644) // crash-safe count
		bootlog(datadir, nodeVersion, "confirm-rollback", fmt.Sprintf("suspect %s, attempt %d/%d", suspect, st.ConfirmAttempts, maxBadBoots))
		go func() {
			if waitHealthy(rpc) {
				// rollback target is healthy => the suspect really was bad
				if suspect != "" {
					os.WriteFile(mk(".badversion"), []byte(suspect), 0o644)
				}
				os.Remove(mk(".suspect"))
				os.Remove(mk(".confirmn"))
				os.Remove(mk(".old"))
				os.Remove(mk(".bad"))
				os.Remove(mk(".envhalt"))
				bootlog(datadir, suspect, "blacklisted", "previous version healthy")
				log.Printf("auto-update: confirmed v%s was bad (previous version is healthy); blacklisted until a newer fix", suspect)
			} else {
				// rollback target stayed alive but never served => not the binary
				clearSuspectAsEnvFault(datadir, suspect, "previous binary alive but never became healthy")
			}
		}()

	case bootEnvFaultConfirmed:
		// The rollback target ALSO crash-looped: the fault is environmental/data,
		// not the binary. Un-blacklist the suspect and pause auto-update.
		clearSuspectAsEnvFault(datadir, readTrim(mk(".suspect")), fmt.Sprintf("rollback target also failed %d boots", st.ConfirmAttempts))

	case bootNormal:
		os.Remove(mk(".old"))
		os.Remove(mk(".bad"))
		os.Remove(mk(".bootn"))
		if envHalted() {
			go func() {
				if waitHealthy(rpc) {
					os.Remove(mk(".envhalt"))
					bootlog(datadir, nodeVersion, "env-recovered", "healthy normal boot")
					log.Print("auto-update: environment recovered; auto-update re-enabled")
				}
			}()
		}
	}
}

// runDiagnose implements -diagnose: a human+machine readable situation report.
func runDiagnose(datadir, p2p, rpc string) {
	fmt.Printf("cereblixd v%s (consensus v%d)\n", nodeVersion, core.NodeConsensusVersion)
	d := selfDiagnose(datadir, p2p, rpc)
	if d.OK {
		fmt.Println("environment:   OK (datadir writable, ports free)")
	} else {
		fmt.Println("environment:   PROBLEM —", d.summary())
	}
	fmt.Println("rpc healthy:  ", rpcHealthy(rpc))
	if autoUpdateDisabled() {
		fmt.Println("auto-update:   OFF (operator-disabled; `cereblixd -autoupdate on` to re-enable)")
	} else {
		fmt.Println("auto-update:   ON (signed, verified, with rollback)")
	}
	fmt.Println("update state:")
	state := false
	show := func(label, suffix string) {
		if v := readTrim(mk(suffix)); v != "" {
			fmt.Printf("  %-16s %s\n", label, v)
			state = true
		}
	}
	show("pending", ".pending")
	show("suspect (bad?)", ".suspect")
	show("blacklisted", ".badversion")
	show("boot attempts", ".bootn")
	show("confirm boots", ".confirmn")
	if exists(mk(".old")) {
		fmt.Println("  .old backup      present")
		state = true
	}
	if envHalted() {
		fmt.Println("  AUTO-UPDATE PAUSED:", readTrim(mk(".envhalt")))
		state = true
	}
	if !state {
		fmt.Println("  clean (no pending/suspect/blacklist)")
	}
	fmt.Println("recent boots:")
	if b, err := os.ReadFile(datadir + "/bootlog.jsonl"); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		for _, l := range lines {
			fmt.Println("  ", l)
		}
	} else {
		fmt.Println("   (no bootlog yet)")
	}
}
