# 0004. Equal-work fork-choice tie-break

## Status

Accepted — consensus v3 (node v2.2.0)

## Context

Fork choice in Cereblix is by **most cumulative work** (`WorkOf(target) = 2^256 /
(target+1)`), not by chain length. That is correct, but it leaves an edge case:
when two competing tips sit at the **same height with exactly equal cumulative
work** (a same-height collision — common when two miners solve a block at nearly
the same instant), neither is "heavier." Different nodes can keep different tips,
and the network stays split until some later block randomly tips the balance —
wasting hashrate on the losing strand and delaying convergence.

## Decision

We will add a **deterministic tie-break**: on an *exact* equal-work collision at
the same height, the block with the **numerically smaller hash** wins. Because the
rule is a pure function of the block hashes, every honest node independently
reaches the same choice with no extra coordination. We pair it with active block
**push** on adoption and a **51% / divergence monitor**, and ship the bundle with
the LWMA difficulty change (ADR 0003) as part of consensus v3.

## Consequences

- Competing equal-work tips converge in a single round instead of lingering as a
  split, so less hashrate is wasted on a doomed strand.
- Deterministic and message-free — no voting, no new network round-trips; it is a
  strict improvement over leaving ties unresolved.
- Folded into the v3 readiness gate alongside LWMA, so it activates split-proof
  with the same supermajority signal.
