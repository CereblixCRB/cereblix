# 0007. `diskMu` outer lock — move bbolt fsync off the chain mutex

## Status

Accepted — node v2.4.1, deployed fleet-wide 2026-06-30. Consensus-identical to v2.4.0.

## Context

After the fleet moved its block store to **bbolt** (an on-disk B+tree replacing
the JSONL file store), a new whole-node freeze appeared. A durable bbolt commit
ends in an **`fsync`**, which on a slow or busy disk can block for a long time.
That `fsync` was being performed while the node held the **chain mutex (`c.mu`)**,
so a single slow disk write stalled *every* reader and writer of chain state —
totally freezing the node, not just slowing persistence. This was the residual
cause behind a late-June wedge that the CPU-side fix in ADR 0006 did not cover.

## Decision

We will introduce a new **`diskMu` outer lock** that moves the bbolt `fsync` (the
durable-commit step) **off `c.mu`**. The chain mutex is released before the
blocking disk sync, so a slow disk degrades *durability latency* but no longer
blocks readers and writers of in-memory chain state. The change is
**consensus-identical** to v2.4.0 — it changes only lock ordering around
persistence — and ships as node v2.4.1.

## Consequences

- A slow or saturated disk no longer total-freezes the node; the node stays live
  and responsive while the commit drains in the background.
- No consensus change, so it rolls across the fleet without coordination, via the
  signed self-update path.
- Completes the freeze-hardening pair: ADR 0006 took the *CPU* work (PoW + signature
  verification) off the lock; this ADR takes the *disk* work (`fsync`) off the lock.
- Adds a second lock to reason about; lock ordering (`diskMu` outer, `c.mu` inner)
  must be preserved to avoid deadlock.
