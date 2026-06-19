# Cereblix HA db-mode pool — symptoms ↔ logs ↔ fixes

This is the field guide for the **fixed** db-mode pool (binary `cereblix-pool.dbmode-fixed`,
sha `004d49b657f9`). The OLD pool.json pool (`999176a4c3cc`) stays installed as the
**emergency backup** (`/opt/cerebra/bin/cereblix-pool` on prod right now).

## What broke on 2026-06-18 (root causes)

1. **MONEY: payouts silently stalled.** `nodeGet` used `http.Get` with NO timeout. When the
   node got busy, `/mempool` (reconcile) or `/balance` (payout) hung forever → `reconcile()`
   blocked → the payout loop never reached `dbLeaderAcquire` → the leader lease stayed
   unacquired (`holder=NULL`) → nobody paid, despite spendable > owed. SILENT.
   - Fix: `nodeClient = &http.Client{Timeout: 10s}`; slow/failed node calls are logged.
   - Defensive: `dbLeaderAcquire` now uses `make_interval(secs => $2)` (no `int || text`
     pgx type-inference gamble that could error the lease).
   - The payout loop no longer `continue`s silently — every skip is logged with the reason.

2. **FALSE ALARM: "pool dead after restart".** `active_miners` is a 5-minute in-memory window
   (`shareEv`) that RESET to 0 on every restart and rebuilt over ~60-90s. Each panic-restart
   reset it again → looked permanently stuck. Miners were fine the whole time.
   - Fix: `shareEv` is snapshotted to `<state>.shareev` every 30s and reloaded on startup —
     `active_miners`/hashrate survive a restart. (Verified: 3→restart→3.)

## Log lines and what they mean

| Log line | Cadence | Watch for |
|---|---|---|
| `HA: db-mode ON ... active=true/false` | startup | which node is the writer |
| `HA: node ACTIVE / STANDBY` | on role change | failover happened |
| `HA: ⚠ role probe ... ERROR` | ~30s while failing | DB unreachable → role frozen |
| `shareEv: restored N ... events` | startup | active_miners survives restart (N>0 = good) |
| `RECONCILE: height=.. owed_total=.. took=NNms` | ~60s | `took` climbing = node/DB lag (precedes a stall) |
| `STATS/2s submits/ACCEPTED/lowdiff/notbound...` | 2s | acceptance health; lowdiff≈feedback is normal |
| `ACTIVE/10s active_miners=.. shareEv_buffered=.. accepted=../s lowdiff=../s role=..` | 10s | **the connection/active gauge** — buffered climbing from 0 after a restart IS the rebuild, not an outage |
| `node: GET <path> SLOW NNms` / `FAILED` | on slow/failed | **early warning of the payout stall** |
| `PAYOUT/cycleN: leader=ok reconcile=.. spendable=.. payable=.. planned=..` | ~60s | payouts running; `leader=ok` = lease held |
| `PAYOUT/cycleN: ⚠ leader-acquire ERROR` | ~60s | **payouts NOT running** — lease/DB problem |
| `PAYOUT/cycleN: not leader` | ~60s | another instance is paying (normal in HA) |
| `PAYOUT/cycleN: ⚠ node balance ERROR` | ~60s | **payouts NOT running** — node problem |
| `PAYOUT/cycleN: ⚠ N miners owed .. but NOTHING planned` | ~60s | coinbase still maturing (or spendable=0) |
| `PAYOUT/cycleN: sent/DEFERRED` | per payout | individual payout outcomes |

## Symptom → first log to check

- "Miners report low-difficulty rejects after a restart" → `STATS/2s` lowdiff vs ACCEPTED, and
  `ACTIVE/10s` shareEv_buffered (rebuilding). If ACCEPTED is climbing, it is recovering — DO NOT restart.
- "active_miners is low / not growing" → `ACTIVE/10s` shareEv_buffered. If it climbs from 0 it is the
  normal post-restart rebuild (now mostly avoided by the snapshot). One restart, then WAIT ~90s.
- "Payouts stopped" → grep `PAYOUT/cycle`. `leader=ok` + `planned=0` with owed>0 → check `node: GET SLOW`
  and `RECONCILE took`. `leader-acquire ERROR` → DB/lease. No PAYOUT line at all → loop stalled (node hang).

## TEST DEPLOYMENT (LIVE since 2026-06-18) — fixed software, isolated, prod untouched

Runs the FIXED binary as real systemd services on the head, fully isolated from prod:
- `cereblix-pool-test.service` → `cereblix-pool.dbmode-fixed`, db-mode, `127.0.0.1:18764`, isolated DB
  `cereblix_pool_test`, throwaway wallet `/opt/cerebra/test/testwallet.txt` (addr `crb13bdbe1a…`),
  state `/opt/cerebra/test/testpool.json`. Launcher `/opt/cerebra/run-pool-test.sh` (keeps the DB pw out of the unit).
- `cereblix-stratum-test.service` → `:3343` → test pool. `ufw` 3343 open.

Use it:
- Watch live: `journalctl -u cereblix-pool-test -f` → `RECONCILE` / `PAYOUT/cycleN leader=ok` / `ACTIVE/10s`.
- Restart-survival (PROVEN): `systemctl restart cereblix-pool-test` → `shareEv: restored N` → active unchanged.
- Inject synthetic miners: `curl -H "X-Credit-Secret: $(cat /opt/cerebra/test/testcred.secret)" "http://127.0.0.1:18764/api/credit?addr=crb1<40hex>&shares=5"`.
- Real-share run: point a CPU miner at `<head-ip>:3343` (mines to the throwaway wallet on the real chain; key is recoverable).
- Scripts: `infra/setup-test-deploy.sh`, `infra/verify-test-deploy.sh`. Latest full backup: `/opt/cerebra/backups/20260618-2040/`.

## Re-cutover to prod — PUSH-BUTTON (prepared + rehearsed 2026-06-18)

The whole cutover is one guarded script (auto-rolls-back to the old pool on ANY failure, so prod is
never left down). It: backs up → stops old pool → fresh-migrates the live pool.json → Postgres
(`cereblix_pool`, aborts on count mismatch) → swaps to `cereblix-pool.dbmode-fixed` + the db-mode
drop-in (`run-pool-ha.sh`, real wallet) → restarts → verifies. Caddy stays direct-proxy (the LB is a
separate later step), so no Caddy/firewall changes.

```
# GO (prod test):
bash /opt/cerebra/cutover-to-dbmode.sh CONFIRM
journalctl -u cereblix-pool -f      # expect 'PAYOUT/cycle1: leader=ok ... planned=N' within ~60s

# ROLLBACK (emergency):
bash /opt/cerebra/rollback-to-oldpool.sh CONFIRM
python3 /opt/cerebra/revert-to-pooljson.py     # then recover db-mode-period earnings into pool.json
```

Expectations after cutover:
- `active_miners` rebuilds ONCE over ~60-90s (the old pool had no shareEv snapshot to hand off) — NORMAL, do NOT restart. After that, the snapshot protects every future restart.
- Validated in lab + on the test stand (`cereblix-pool-test`): leader acquires, owed reconciles from chain, payouts log/send, active survives restart.
- Pre-flight dry-run anytime (zero risk): `bash /tmp/dryrun-migration.sh` (migrates live pool.json into the TEST DB + runs the fixed pool on it).
