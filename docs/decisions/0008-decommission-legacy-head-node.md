# 0008. Decommission the legacy all-in-one "head" node

## Status

Accepted — completed 2026-06-21. (Operational/topology decision; no consensus impact.)

> Scope note: this ADR records *why* the topology changed. Per the boundary in this
> directory's README, it deliberately omits server addresses, key-custody locations
> and other operator-internal specifics — those live in the private `cereblix-ops` repo.

## Context

The network originally ran on a single legacy **"head"** node — one overloaded box
that did almost everything at once: a public P2P/RPC node, the release/checkpoint
**authority signer**, the OTC desk (handling real funds), the faucet treasury,
translator bots, and an orphaned database. That made it simultaneously a **single
point of failure** and the **most security-sensitive box on the network**.

Two problems converged: its node RPC began wedging under connection churn (it was
simply doing too much), and concentrating the authority key plus real treasury
funds on one always-public, overloaded host was an unacceptable risk profile.

## Decision

We will **decommission the head node** and replace the monolith with
**role-separated hosts**:

- The release/checkpoint **authority signer** and other sensitive services move to
  a host kept **internet-invisible** (no public inbound) — consistent with the
  authority key being held off the public network (ARCHITECTURE.md §5).
- The **web origin** moves to a separate **hardened, lightweight** host behind the
  CDN.
- Node, pool and database roles are spread across dedicated hosts with a
  **high-availability** pool/database setup, removing the single point of failure.

Crucially, this is a **careful migration, not a power-off**: because the head held
the authority key, the OTC seed and the faucet treasury, we first back up its
secrets, move every role and verify it, remove the host from DNS / seed lists /
peer lists / the HA quorum, and **only then** power it off.

## Consequences

- The single point of failure and the most-sensitive single box are both
  eliminated; the authority signer no longer sits on an always-public host.
- A new operating discipline follows from the split: the host that holds the
  authority signer must stay invisible, and the web origin must stay light.
- More hosts to operate and a real HA setup to maintain — more moving parts in
  exchange for resilience.
- The migration-first sequencing (back up, move, verify, de-register, *then* power
  off) is the reusable lesson: a box holding keys and funds is never "just turned
  off."
