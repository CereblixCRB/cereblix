# 0005. Gated v4 deep-reorg recovery hardfork (≥95% signal)

## Status

Accepted — consensus v4 (node v2.4.0, 2026-06-28). **Gated and currently dormant**
(awaiting the activation signal). Amends [ADR-0002](0002-checkpoint-anchored-anti-51.md).

## Context

The `MaxReorgDepth = 100` cap (ADR 0002) correctly rejects any reorg deeper than
100 blocks — that is what kills rewrite-from-genesis attacks. But the cap is
blind to *who* is behind: an **honest** node that itself falls more than 100
blocks behind (e.g. after the FD-exhaustion wedge addressed in ADR 0006, or a long
outage) cannot re-adopt the canonical chain, because doing so *is* a >100-block
reorg. Recovery then required a manual chain reseed by an operator. We wanted
**autonomous** re-convergence — without re-opening the deep-reorg attack the cap
was built to close.

## Decision

We will add a **consensus v4 hardfork**: a node that is further than `-maxreorg`
behind **may** exceed the reorg cap, but **only** to adopt a candidate chain that
carries a valid **authority-signed checkpoint anchor**. An anonymous attacker
cannot forge that signature, so the deep-reorg attack stays closed; only the
honest, signed canonical chain qualifies for deep recovery.

Activation reuses the readiness gate (ADR 0003) with its **own** window: it locks
in only at **≥95% of the last 50 blocks** signalling v4 (the pool stamps
`crbnode/4`). The frozen fee-market (v2) and LWMA (v3) gates are untouched. Below
activation the rule is **byte-identical to v3**, so it is dormant and safe until
the supermajority is reached.

## Consequences

- A stuck honest node can re-converge to the authority-signed chain on its own —
  no manual reseed — while the 51% guard and split-proofing are preserved.
- Refines, rather than replaces, ADR 0002: the cap still governs the general case;
  v4 only carves out the signed-anchor recovery path. It keeps the same dependency
  on the off-network authority key.
- It stays harmless while dormant; reaching 95% requires external pools (e.g.
  rplant) to run v2.4.0+, so distribution — not a flag day — drives activation.
