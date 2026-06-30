# 0003. LWMA difficulty (90-block window), readiness-gated

## Status

Accepted — consensus v3 (node v2.2.0); activated and locked in 2026-06.

## Context

The launch difficulty algorithm was a legacy 20-block windowed-average retarget
(average target × actual/expected time, clamped to a [1/4 .. 4×] band). On a young
network with volatile hashrate it **oscillated** — over- and under-shooting as
miners joined and left — producing uneven block times.

Changing the retarget rule is consensus-breaking: two nodes computing a different
target for the same height will disagree on which blocks are valid. So the new
rule cannot simply flip on at a height while old nodes are still live, or the
network splits.

## Decision

We will switch difficulty to an **LWMA** (Linearly Weighted Moving Average) over
the last **90 block solvetimes**, weighting recent blocks more so difficulty
tracks hashrate changes quickly without the legacy oscillation. Each solvetime is
clamped to `[1 s, 600 s]` as a time-warp defense.

The change is **readiness-gated** (BIP9-style) as **consensus v3**: every block
advertises its node's consensus version in a free-form coinbase field, and the
LWMA rule locks in only once a supermajority (**90 of the last 100 blocks**)
signals v3. The gate is **floorless** — it activates purely on the supermajority
signal, which a large external pool cannot reach until it too upgrades. The gate
measures a **frozen** required-version constant, never the moving node version, so
a later version bump cannot retroactively re-date an already-activated fork.
Before activation, nodes use the legacy 20-block retarget **byte-for-byte**, so
chain history stays valid.

## Consequences

- Difficulty responds smoothly to hashrate swings; block time tracks the 60 s
  target far better.
- The rollout is split-proof: a minority running the new code cannot activate it
  alone and so can never strand the majority on a heavier fork. The cost is that
  activation requires most hashrate — including independent pools — to upgrade
  first; it cannot be forced on a schedule.
- Establishes the reusable readiness-gate pattern later used by the v4 hardfork
  (ADR 0005), combined with the self-updating node so rollout needs no manual
  coordination.
