# Release & versioning

How Cereblix tags, builds, signs and ships its binaries. This is the policy doc;
the secret-bearing mechanics (authority key handling, fleet rollout order) live in
`.secrets/RELEASE_RUNBOOK.md` and are out of scope here.

Scope: this is the **public** repo (`CereblixCRB/cereblix`) - node, miners,
wallets, Stratum bridge. Operator-internal components (the pool, faucet, OTC,
watchtower, checkpoint signer, web frontend) live in the private `cereblix-ops`
repo and follow the **same** conventions there. See [`../ARCHITECTURE.md`](../ARCHITECTURE.md)
for protocol behaviour and [`../README.md`](../README.md) for the user guide.

---

## 1. Per-binary tags

Each shippable artifact is versioned **independently** with its own SemVer line
and its own `<component>/vX.Y.Z` git tag. The components ship on different cadences
(a wallet UI fix should not force a node version bump, and vice-versa), so they do
not share one repo-wide version.

| Tag prefix       | Component              | Source path                                  | Repo        |
|------------------|------------------------|----------------------------------------------|-------------|
| `node/vX.Y.Z`    | Full node daemon       | `cmd/cereblixd`                              | public      |
| `miner/vX.Y.Z`   | Reference CPU miner    | `cmd/cereblix-miner`                         | public      |
| `wallet/vX.Y.Z`  | Wallets (CLI + GUI)    | `cmd/cereblix-wallet`, `desktop/`, `wallet-android/` | public |
| `stratum/vX.Y.Z` | Stratum bridge         | `cmd/cereblix-stratum`                        | public      |
| `unm/vX.Y.Z`     | UNM (Ultra Native Miner) | `unm/`                                      | public      |
| `pool/vX.Y.Z`    | Mining pool            | `cmd/cereblix-pool`                          | private (`cereblix-ops`) |

`pool/*` tags are cut in the private operator repo because the pool code only
lives there - never commit pool/faucet/OTC/checkpoint/web code into this public
repo. The convention is identical; only the repo differs.

### SemVer meaning per component

- **MAJOR** - incompatible change for that component's users (e.g. a wallet file
  format break, a removed CLI flag, a Stratum protocol break).
- **MINOR** - backward-compatible features.
- **PATCH** - backward-compatible fixes.

### `node/*` is special: software version vs. consensus version

The node carries **two** independent version numbers, and they must not be
conflated:

- **Software version** - the SemVer release line. It is the constant
  `nodeVersion` in `cmd/cereblixd/update.go` (currently `2.4.2`) and is what the
  `node/vX.Y.Z` tag and the auto-update manifest track. It changes on every node
  release, consensus-affecting or not.
- **Consensus version** - `core.NodeConsensusVersion` in `core/upgrade.go`
  (currently `4`). It is the protocol capability the node advertises in the
  coinbase (`crbnode/<n>`) and only increments when a new readiness-gated rule is
  added (v2 = fee market, v3 = LWMA difficulty, v4 = checkpoint-anchored
  deep-reorg recovery). Many `node/*` releases ship with the **same** consensus
  version.

A consensus change therefore looks like: bump `NodeConsensusVersion`, add a
**frozen** per-fork required-version constant (never reference the moving
`NodeConsensusVersion` from a gate), ship it in a normal `node/vX.Y.Z` release,
and let the network activate it by signal (see `ARCHITECTURE.md` 4.5). No flag
day, no release branch.

> Frozen consensus strings (`cerebra-tx-v1`, `cerebra-txroot-v1`, the genesis
> message, the epoch-0 seed, the `X-Cerebra-Peer` header, the `crb1` address
> prefix) are historical and are **never** renamed - doing so forks the live
> chain. Tagging/versioning never touches them.

---

## 2. Trunk-based flow

Cereblix uses **trunk-based development**, not gitflow:

- **`main` is the trunk** and is always releasable. The reproducible release build
  is cut from a commit on `main`.
- **Short-lived feature branches** off `main`, merged back fast. No long-lived
  `develop` or `release/*` branches to maintain.
- **A release is just a tag** on a `main` commit (`node/v2.4.1`, `wallet/v1.3.0`,
  ...). The tag - not a branch - is the release artifact's anchor.
- **Hotfix = land the fix on `main`, cut a new PATCH tag.** Because consensus
  upgrades are readiness-gated (BIP9-style), even a protocol change rolls out as a
  normal forward release that activates on signal - so there is no need for a
  parallel maintenance branch or a coordinated cutover.
- **Commit author is always**
  `CereblixCRB <157488947+CereblixCRB@users.noreply.github.com>` - never a
  personal identity.

### Pre-tag checklist

Run before cutting any tag:

```sh
# go = C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin\go.exe
<go> test ./neuromorph     # VM determinism is consensus-critical - must pass
<go> test ./...            # full suite
<go> vet ./...
```

For a `node/*` release also confirm the **software version actually increased**
(`nodeVersion` in `cmd/cereblixd/update.go`): the auto-updater only ever moves
**forward**, so a non-increasing version silently ships to nobody.

---

## 3. Signed releases

Two **separate** signing layers, with different jobs. Do not collapse them.

### 3a. Developer / supply-chain signing (new)

This proves *who* built the published artifacts and that the bytes are intact -
provenance for anyone downloading from GitHub or `cereblix.com`.

1. **One `checksums.txt` per release** covering **every** binary in that release.
   Generated right after the build:

   ```sh
   ( cd dist && sha256sum * > checksums.txt )
   ```

   Users verify with:

   ```sh
   sha256sum -c checksums.txt
   ```

2. **GPG-signed git tags.** Tags are annotated and signed with the maintainer's
   GPG key:

   ```sh
   git tag -s node/v2.4.1 -m "cereblixd 2.4.1"
   git push origin node/v2.4.1
   ```

   Verify with `git verify-tag node/v2.4.1`. Optionally also detach-sign the
   checksum manifest so a single signature covers every artifact's hash:

   ```sh
   gpg --armor --detach-sign checksums.txt   # -> checksums.txt.asc
   ```

   Publish the maintainer GPG public key (in-repo and/or on a keyserver) so the
   tag and `checksums.txt.asc` are independently verifiable.

### 3b. Consensus-bound signing (unchanged - keep `authority.key`)

The in-protocol Ed25519 **`authority.key`** stays exactly as it is. It is a
**different trust domain** from GPG and is **not replaced** by it:

- It signs the auto-update manifest (`upgrade.json` / `core.UpgradeManifest`) and
  the chain **checkpoints**. Every node verifies these against `AuthorityPubKey`,
  which is **compiled into the binary** - so update and checkpoint enforcement are
  trustless *inside the running network*, with no human in the loop
  (`ARCHITECTURE.md` 5 and 6.10).
- The manifest's `Version` field must **strictly increase** and is SHA-256-matched
  against the downloaded binary before an atomic, self-healing swap (crash-loop
  rollback + bad-version blacklist).

The split, stated plainly:

| Layer            | Key            | Signs                                  | Verified by                  | Purpose                          |
|------------------|----------------|----------------------------------------|------------------------------|----------------------------------|
| Supply chain     | maintainer GPG | git tags, `checksums.txt`              | humans, at download time     | who built it / bytes intact      |
| Consensus / OTA  | `authority.key`| `upgrade.json`, checkpoints            | every node, automatically    | trustless auto-update + finality anchor |

Both private keys live only under `.secrets/` (gitignored), off the network.
Adding GPG does **not** touch the auto-update or checkpoint path - `authority.key`
remains the consensus-bound signer.

### Release flow (node example)

```sh
# 1. main is green (see the pre-tag checklist) and nodeVersion was bumped > current
# 2. reproducible Linux build (the exact flags used to ship cereblixd)
GOTOOLCHAIN=go1.25.0 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  <go> build -trimpath -ldflags '-s -w' -o dist/cereblixd-linux-amd64 ./cmd/cereblixd
#    ... repeat for the other target OS/arch binaries ...

# 3. checksum manifest over every artifact
( cd dist && sha256sum * > checksums.txt )

# 4. signed git tag
git tag -s node/v2.4.1 -m "cereblixd 2.4.1"
git push origin node/v2.4.1

# 5. sign + publish upgrade.json with authority.key, Version = 2.4.1 (must increase)
#    -> see .secrets/RELEASE_RUNBOOK.md for the manifest + fleet rollout steps

# 6. upload the binaries + checksums.txt (+ optional checksums.txt.asc) to the release
```

---

## 4. Out of scope (intentionally)

- **SLSA provenance** and **SBOM** generation are deliberately **not** adopted.
  For a small team shipping a pure-Go, near-zero-dependency tree, the
  `sha256` + GPG combination above already covers artifact integrity and build
  provenance for the realistic threat model, while SLSA/SBOM tooling adds
  significant maintenance for little marginal assurance here. Revisit only if the
  project takes on a real dependency surface or distro/package-manager
  distribution.
