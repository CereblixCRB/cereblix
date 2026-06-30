# 0002. Checkpoint-anchored 51% resistance (MaxReorgDepth=100 + signed checkpoints)

## Status

Accepted — network bootstrap phase (consensus v3, 2026-06). Amended by
[ADR-0005](0005-gated-v4-deep-recovery-hardfork.md).

## Context

A day-old Proof-of-Work chain cannot be cryptographically final against a >50%
attacker — real finality only accrues with accumulated hashrate over time. While
the network's hashrate is small, the cheap catastrophic attack is a deep reorg
(rewrite-from-genesis or a long double-spend). We need to kill that attack and buy
time, **without** building permanent centralization into the protocol.

## Decision

We will ship layered 51% mitigations — decentralized by default, plus an authority
checkpoint for the bootstrap phase:

- **Max reorg depth** (`-maxreorg`, default **100**): any reorg that would rewrite
  more than N blocks is rejected outright, killing rewrite-from-genesis attacks.
- **Reorg-cost penalty** (`-reorg-penalty`, optional): deeper reorgs must carry
  disproportionately more work.
- **Authority checkpoints** (bootstrap only): an off-network authority key signs
  the canonical tip; nodes pull signed checkpoints from peers, verify them against
  a public key compiled into the binary, and refuse any chain that conflicts with
  one (no reorg may cross a checkpoint). The signer is the standalone
  `cmd/cereblix-checkpoint` tool; the private key is held off the network.

We will **not deepen** the checkpoint cadence beyond its conservative setting —
maximum anti-51% coverage with minimal signing footprint. The checkpoint is a
**deliberate, transparent, removable** centralization: it can be retired (sign
nothing, or ship a binary without the key) as independent nodes and hashrate grow
into real finality. It does **not** prevent anyone forking the open-source code
into a separate coin — it only protects *this* chain's history from rewrites.

## Consequences

- The cheap deep-rewrite and cross-checkpoint reorg attacks are removed during the
  vulnerable bootstrap window; shallow double-spends get more expensive.
- A real but bounded trust assumption: nodes trust the compiled-in authority key
  from first run. This is documented openly as an early-phase trade-off, not hidden.
- An operational dependency on the authority key's secrecy (kept off-network).
- The `MaxReorgDepth` cap later needed a carefully-scoped exception so an honest
  node that legitimately falls >100 blocks behind can still recover — addressed,
  without weakening the guard against attackers, in ADR 0005.
