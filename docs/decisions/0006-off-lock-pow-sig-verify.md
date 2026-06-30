# 0006. Off-lock PoW + signature verification (FD-exhaustion fix)

## Status

Accepted — node v2.4.0, deployed fleet-wide 2026-06-28. Consensus-identical to v3.

## Context

Nodes were periodically wedging: file-descriptor exhaustion (`accept4: too many
open files`), a large pile of `CLOSE_WAIT` sockets, and a "fork-strand" sync stall
where the node would not recover until restarted ("only restart helps").

The root cause was a lock-contention pileup, not a consensus bug. The expensive
parts of block/transaction validation — the **NeuroMorph PoW hash** (~4 ms each)
and **ed25519 signature verification** — were being performed while the node held
the **chain write-lock**. Under load that serialized everything behind slow CPU
work: connections could not be serviced or closed, so sockets and file descriptors
accumulated until the node ran out and froze.

## Decision

We will move **PoW verification and transaction-signature verification off the
chain write-lock** — compute them before/outside the critical section, and take
the lock only for the short state-mutation step. The change is **consensus-
identical** to v3 (it reorders *when* work happens, not *what* is accepted), so it
ships as Stage 1 of v2.4.0 with no fork. We pair it with sync hardening
(doomed-tip memoization, a `healthyReject` path so a losing / too-deep / raced fork
cannot evict healthy peers), a lock-free `/p2p/tip`, peer-health backoff, and a
liveness watchdog (`-stall-restart`).

## Consequences

- The pileup is gone: in production, open file descriptors dropped from
  ~2058/65536 to ~45–229 and `CLOSE_WAIT` from ~5000 to ~1; the "only restart
  helps" wedge no longer occurs.
- No consensus impact — safe to roll across the fleet without coordination.
- This fixed lock contention from *CPU* work. A second, distinct freeze caused by
  *disk* work under the same lock was found later and is addressed separately in
  ADR 0007.
