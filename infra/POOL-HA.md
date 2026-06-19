# Cereblix Pool — Production HA (Postgres-backed db-mode)

Status: **SHIPPED & LIVE** (2026-06-19). The pool runs in db-mode on the head with a hot-standby on
pg-95, behind a health-checked Caddy load balancer. This replaces the old single-node `pool.json`
pool. (The previous "PARKED — do not cut over" handoff is obsolete and was removed.)

## Why
The original db-mode attempt collapsed under real load because it did a **synchronous Postgres
INSERT per accepted share** — the classic mining-pool anti-pattern (MPOS's `shares` table). High-load
pools (open-ethereum-pool, MiningCore) keep the hot path in memory/Redis and write the DB in
aggregates. We adopted that split.

## Architecture
Two tiers, the canonical mining-pool HA shape:

- **Hot tier — in memory (BOTH modes), zero per-share DB I/O.** The PPLNS payout window, the
  per-miner extranonce map and the rolling share-stats window live in process memory. Each is
  snapshotted whole to ONE Postgres row periodically (`snapshotLoop`, ~15-20s) + on each block, and
  on a graceful shutdown (`installShutdownFlush`, SIGTERM). On startup/failover they reload from
  Postgres (replicated by Patroni → a promoted standby resumes on the same state).
- **Money tier — Postgres `cereblix_pool` (replicated).** `earned` (credited per block, batched in
  one tx), `inflight`, `payouts`, the single-writer `leader` lease, `blocks_found` (meta), plus the
  hot-tier snapshot rows. Owed/paid are re-derived from the on-chain payout history (`reconcile.go`),
  so the chain is the ultimate source of truth.

**Money safety (no double-pay across failover):** only the leader-lease holder pays; a sent payout's
gross is held in `inflight` until the chain confirms it; `reconcile()` adopts our still-pending
mempool txs as inflight; and all payouts share one wallet nonce, so the network rejects duplicates.

## Components (live)
- **Head pool** `cereblix-pool` (db-mode, `127.0.0.1:18754`) — the normal ACTIVE pool. Role
  (active/standby) is auto-derived from `pg_is_in_recovery()`; `/api/health` 200=active / 503=standby.
- **Standby pool on pg-95** (`13.140.142.95`, WG `10.10.0.2:18754`, `POOL_LISTEN` drop-in) — same
  binary, same wallet, db-mode against the local replica → STANDBY until Patroni promotes pg-95.
  Reach it with `tools/srv-nodes.py 13.140.142.95` (key `~/.ssh/vfs_nodes`). Chain symlink:
  `/var/lib/cerebra/blocks.jsonl → /opt/cereblix/data/blocks.jsonl`.
- **Caddy LB** (`/etc/caddy/Caddyfile`): `/pool/*` AND an internal `http://127.0.0.1:18755` both
  `reverse_proxy 127.0.0.1:18754 10.10.0.2:18754 { lb_policy first; health_uri /api/health;
  health_status 200 }` — route only to the active (primary) pool; the standby (503) is skipped.
- **Stratum bridge** `cereblix-stratum -pool http://127.0.0.1:18755/api` — points at the internal LB
  so stratum miners follow a failover too. It pushes a fresh job on tip/target **or header** change
  (the header-refresh fix), so a pool switch refreshes miners' work without waiting for a block.
- **Postgres HA**: WireGuard mesh + etcd(3) + Patroni(2). pg-head Leader / pg-95 Replica.

## Operating rules
- **The ACTIVE pool must live on the HEAD.** Running active on pg-95 is degraded — every
  getwork/submit then crosses the WireGuard hop (bridge→:18755→.95), producing stale shares (accept
  drops, submits spike). pg-95 is a **failover target only**; switch back to the head ASAP.
- **A failover MOVES serving** to the standby and miners stay connected (no crash), but there is a
  ~1-2 min vardiff re-convergence transient (low accept until it settles / a block lands). This is a
  known residual, not a crash. Don't run failover tests casually — each is a real ~1-2 min disruption.
- After a failover, switch the primary back to the head once pg-head has settled as a healthy replica
  (~2 min): `bash infra/switchback-retry.sh` (a switchover attempted too soon fails `412 no good
  candidates`). Manual `patronictl switchover` can take ~100s.

## Operations cheatsheet
- Build canonically on the server: `cd /opt/cerebra/src && GOTOOLCHAIN=go1.25.0 CGO_ENABLED=0 \
  /usr/local/go/bin/go build -trimpath -o <out> ./cmd/cereblix-pool/`
- Schema: `infra/pool-schema.sql` (earned/inflight/payouts/leader/meta + pplns_snapshot /
  extranonce_snapshot / shareev_snapshot). Migrate `pool.json`→Postgres: `infra/migrate-pool-state.py`.
- Cut over an old pool → db-mode: `infra/cutover-to-dbmode.sh CONFIRM` (auto-rollback on error).
  Roll back: `infra/rollback-to-oldpool.sh CONFIRM` then `infra/revert-to-pooljson.py`.
- Stand up / refresh the pg-95 standby: `infra/setup-std95.sh` / re-deploy its binary.
- Caddy LB: `infra/deploy-caddy-lb.sh` + `infra/deploy-stratum-lb.sh` (validate + reload + rollback).
- HA failover test (rare, disruptive): `infra/failover-verify-full.sh`.
- Symptom ↔ log field guide: `infra/dbmode-symptoms-logs.md`.

## Future polish (not blocking; do on the rig in a calm window)
- Eliminate the failover transient: persist per-miner vardiff across reconnects; and/or speed up the
  Patroni switchover (`loop_wait`); and/or let the standby serve getwork too (payouts stay leader-only).
- Full head-DEATH failover (vs PG failover) needs a Cloudflare LB / DNS failover across head+.95
  origins (the head's Caddy + stratum bridge are a SPOF for a total head loss).
