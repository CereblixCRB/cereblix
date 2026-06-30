# Architecture Decision Records (ADRs)

This directory holds the **why** behind Cereblix's architecturally-significant
decisions, recorded next to the code instead of living only in someone's head (or
a machine-local note). When you wonder "why is it done *this* way?", the answer
should be here.

## What an ADR is

One short Markdown file per decision that meaningfully shaped the system — a
consensus rule, a security policy, a structural split, a technology choice. We use
the lightweight **Nygard format**: five sections — **Title / Status / Context /
Decision / Consequences** (see [`0000-template.md`](0000-template.md)).

An ADR captures the *moment of choosing*: the problem, the forces in tension, the
call we made, and what we traded away. It is **not** a spec or a how-to — for "how
it works today" read [`ARCHITECTURE.md`](../../ARCHITECTURE.md). The two
complement each other: ARCHITECTURE.md is the live map; ADRs are the history of
how the map got that shape.

## The practice (deliberately low-ceremony)

- **One file per decision**, numbered sequentially: `NNNN-short-title.md`.
- **~90 seconds to write.** Copy the template, fill in five headings, keep it
  under a page. If it takes longer, you are writing a spec — stop and trim.
- **No tooling.** No generator, no database, no plugin. Plain Markdown a human
  edits and Git versions. That is the whole system.
- **Immutable once Accepted.** Don't rewrite history. If a decision changes,
  write a *new* ADR that supersedes the old one and flip the old one's Status to
  `Superseded by [ADR-XXXX]`. The record of *what we believed when* is the value.
- **Write it when the decision is made**, while the context is fresh — not months
  later when the rationale has evaporated.

## Scope boundary (important)

This directory lives in the **public** `cereblix` repo, so ADRs here describe the
**open-source coin**: consensus, the node, the protocol, the wallets. Keep
operator-internal specifics — server IPs, the WireGuard topology, key custody
locations, pool/OTC/faucet/infra mechanics — **out of these files**; that
rationale belongs in the private `cereblix-ops` repo. An ADR can name *that* a
piece of infrastructure exists and why, without publishing *where* it runs.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-public-private-repo-split.md) | Public / private repository split | Accepted |
| [0002](0002-checkpoint-anchored-anti-51.md) | Checkpoint-anchored 51% resistance (MaxReorgDepth=100 + signed checkpoints) | Accepted |
| [0003](0003-lwma-difficulty-gated-rollout.md) | LWMA difficulty (90-block window), readiness-gated | Accepted |
| [0004](0004-equal-work-forkchoice-tiebreak.md) | Equal-work fork-choice tie-break | Accepted |
| [0005](0005-gated-v4-deep-recovery-hardfork.md) | Gated v4 deep-reorg recovery hardfork (≥95% signal) | Accepted (gated, dormant) |
| [0006](0006-off-lock-pow-sig-verify.md) | Off-lock PoW + signature verification (FD-exhaustion fix) | Accepted |
| [0007](0007-diskmu-fsync-off-chain-mutex.md) | `diskMu` outer lock — move bbolt fsync off the chain mutex | Accepted |
| [0008](0008-decommission-legacy-head-node.md) | Decommission the legacy all-in-one head node | Accepted |
| [0009](0009-gui-wallet-architecture.md) | GUI wallet architecture (Wails desktop + gomobile Android) | Accepted |

New ADR? Copy `0000-template.md`, take the next number, add a row above.
