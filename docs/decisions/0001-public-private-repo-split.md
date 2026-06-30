# 0001. Public / private repository split

## Status

Accepted — 2026-06-19

## Context

Cereblix began as a single repository that held both the open-source coin and the
operator-internal services. Those are two very different things with conflicting
requirements:

- The **coin** wants to be maximally open: MIT-licensed, forkable, auditable,
  "mine it, fork it, run your own node." Openness *is* the product.
- The **operator services** (mining pool, faucet, OTC/exchange daemon and mixer,
  checkpoint signer, update-manifest tooling, deployment/infra, marketing and the
  web front-ends) encode anti-abuse logic, treasury handling and operational
  topology. Publishing them helps attackers and leaks how the live network is run.

Mixing the two in one repo meant operator code was one careless commit away from
the public internet. That risk was not hypothetical: an internal front-end
(`web/`) once leaked into the public history and forced a head-history rewrite to
remove it. A clean structural boundary was needed.

## Decision

We will maintain **two repositories** with a hard boundary between them:

- **Public — `CereblixCRB/cereblix`**: the open-source coin *only* — `cereblixd`,
  the miner, the CLI wallet, the WASM hasher, the stratum bridge, `core/`,
  `node/`, `neuromorph/`, `hiveos/`, `deploy/`, and the GUI wallets (`desktop/`,
  `wallet-android/` — see ADR 0009).
- **Private — `CereblixCRB/cereblix-ops`**: everything operator-internal — the
  pool, watchtower, OTC daemon + mixer, faucet, checkpoint tooling, update
  manifest, `infra/`, marketing, and the entire `web/` tree.

Hard rules: pool / faucet / OTC / mixer / watchtower / checkpoint / manifest /
infra / web code is **never** committed to the public repo. Secrets live **only**
in the gitignored `.secrets/` directory and never leave it. The commit author is
always `CereblixCRB <157488947+CereblixCRB@users.noreply.github.com>`.

## Consequences

- Clear license and trust story: the coin is fully open and forkable; the
  operational attack surface is not published.
- The public repo is safe to mirror and fork; a self-hosted Gitea mirror provides
  insurance for both repos.
- Cost: two clone targets and two build setups; contributors and tooling must know
  which repo a change belongs in. The boundary is a discipline, enforced by
  reviewers and by this rule, not by the tooling itself.
- The historical `web/` leak was *not* purged from old public history or the one
  existing fork (no secrets were in it, and a rewrite would be risky and
  incomplete); the split prevents *recurrence* rather than erasing the past.
