# 0008. RC6 sync-wedge hardening — lock-free `/p2p/subscribe`, self-healing tip snapshot, fleet-wide `-stall-restart`

## Status

Accepted — node v2.4.2, deployed fleet-wide 2026-07-01 (all 10 nodes incl. the
kolektiv pool). **Consensus-identical to v2.4.0/v2.4.1** (non-consensus; no
block-validation, PoW, difficulty, reorg, fee-market, v4-signaling or wire change).

## Context

On 2026-07-01 the pool-side fleet wedged: eight nodes fell behind the canonical
chain and could not catch up, while advertised nodes accumulated 1000–2766
half-closed (`CLOSE_WAIT`) sockets. It was **not a fork** — every stuck tip was a
byte-identical ancestor of the canonical chain; the nodes were simply lagging and
their sync machinery was stuck. An adversarial multi-agent review pinned three
distinct, code-confirmed defects (and refuted a fourth, plausible-sounding one):

1. **CLOSE_WAIT amplifier.** `/p2p/subscribe` ended with `writeJSON(w, n.myTip())`,
   and `myTip()` takes the chain read lock. During a chunked catch-up the adopt
   path holds the chain write lock across a whole `adoptChunk` (256 blocks), so a
   woken subscribe handler blocks in `myTip()` *after* its `select` has stopped
   watching the client — the connection is never closed, and it pins a
   `limitListener` slot (cap 1024). Only advertised nodes have many inbound
   subscribers, so this appeared as thousands of `CLOSE_WAIT` on public nodes and
   ~3 on the invisible CORE (the split that identified it).
2. **Stale advertised tip.** The lock-free `/p2p/tip` snapshot is refreshed only
   by the `OnNewBlock` callback. A **panic inside the bbolt commit** (under FD/disk
   pressure) unwinds past that callback — the in-memory chain keeps advancing while
   the snapshot freezes — and the panic is swallowed by the sync path's `recover()`.
   The node then advertised a tip hundreds of blocks stale.
3. **No autonomous recovery for a "behind-and-stuck" node.** The liveness watchdog
   detected the stall but only logged and reset peer backoff; nothing restarted a
   node whose HTTP layer was still responsive. The only thing proven to recover the
   fleet was a manual `systemctl restart`.

The **refuted** hypothesis was that ahead-peers were being *evicted* (20 failed
contacts → `dropPeer`): the watchdog's `resetPeerBackoff` zeroes the failure count
every 30 s, so the eviction threshold is never reached. We did not build a fix for
a strand that does not occur.

The deep root of defect (2)/(3) — *why* a commit hangs and how it stalls the sync
loop — is **not yet proven** (it needs a goroutine dump from a live wedge) and is
explicitly out of scope here: rushing a commit-path rewrite into a fleet consensus
binary is exactly the class of change that caused the RC5 and RC6 incidents.

## Decision

Ship v2.4.2 as a minimal, non-consensus **hardening + safety-net** release:

- **Serve `/p2p/subscribe` from the lock-free `tipSnap`**, identical to `/p2p/tip`.
  The subscribe response is only a gossip hint (the subscriber re-validates every
  pulled block), so this removes the last chain-lock dependency from the hot P2P
  read path and eliminates the `CLOSE_WAIT`/listener-slot pileup.
- **Re-publish the tip snapshot once per `SyncLoop` tick.** Even if an `OnNewBlock`
  callback is missed, `/p2p/tip` and `/p2p/subscribe` re-converge to the true tip
  within ~3 s.
- **Clear the doomed-tip memo from the watchdog only** (never on the sync path,
  which would re-open the RC4 refetch storm) so a concurrent-race mis-memo of the
  winning chain cannot permanently strand a node.
- **Enable `-stall-restart` fleet-wide** with a systemd crash-loop guard, and lower
  the escalation window from 30 min to 20 min. This makes the node self-perform the
  restart that manually recovered the fleet, covering **any** residual wedge —
  including the still-unproven deep freeze — without a risky commit-path rewrite.

## Consequences

- The confirmed `CLOSE_WAIT` amplifier and the stale-`/p2p/tip` defect are gone;
  the fleet no longer pins sockets or advertises a frozen tip during catch-up.
- A node that is genuinely stuck (behind on work, height flat ≥ 20 min) restarts
  itself and re-syncs in seconds, instead of hanging until a human notices.
- `-stall-restart` **requires** the systemd guard (`Restart=on-failure` +
  `StartLimitIntervalSec`/`StartLimitBurst`); without it a persistently-broken node
  would reboot-loop. The guard is installed on every unit (drop-in
  `zz-stall-restart.conf`).
- This is a symptom-and-safety-net release, **not** a root-cause fix for the deep
  commit/`SyncLoop` freeze. The P0 follow-up remains: capture a goroutine dump on
  the next wedge, then bound/park-proof the bbolt commit (and convert the
  commit-path panic to a logged error instead of unwinding past `OnNewBlock`) in a
  separately-reviewed release.
- Non-consensus, so it rolls across the fleet in any order via the signed
  self-update path. Completes the liveness-hardening series after ADR 0006 (CPU
  off-lock) and ADR 0007 (`fsync` off-lock).
