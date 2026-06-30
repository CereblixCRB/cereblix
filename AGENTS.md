# AGENTS.md — Cereblix (public coin repo)

## Project overview

Cereblix (ticker **CRB**) is a CPU-only Proof-of-Work cryptocurrency written from
scratch in Go (standard library only, near-zero external deps) on the
**NeuroMorph** self-mutating PoW VM — the algorithm rebuilds its own VM semantics
every 4096-block epoch from chain entropy, giving lifelong ASIC resistance; a
64 MiB epoch dataset adds memory-hardness. Account/nonce model, ed25519 `crb1`
addresses, LWMA difficulty, 60 s blocks, 50 CRB reward, ~105.12M max supply, zero
premine. The node (`cereblixd`) is a single dependency-free binary that
self-updates from an authority-signed manifest; consensus is readiness-gated
(BIP9-style) and currently **v4**. Network launched 2026-06-11; live at
https://cereblix.com. See `ARCHITECTURE.md` for the full spec.

## Repo boundary

This is the **PUBLIC** repo `CereblixCRB/cereblix` — the open-source coin ONLY:
`cmd/cereblixd`, `cmd/cereblix-miner`, `cmd/cereblix-wallet`, `cmd/cereblix-wasm`,
`cmd/cereblix-stratum`, `core/`, `node/`, `neuromorph/`, `unm/`, `hiveos/`,
`deploy/`, and the GUI wallets `desktop/` (Wails) and `wallet-android/` (gomobile
+ Kotlin).

**YOU MUST NEVER** commit operator-internal code to this repo. Pool, faucet, OTC,
mixer, watchtower, checkpoint, manifest, `infra/`, marketing, and the entire
`web/` frontend (marketing site + web wallet) live **ONLY** in the PRIVATE repo
`CereblixCRB/cereblix-ops`. This exact mistake — `web/` leaking into public —
already forced a HEAD-history rewrite. `web/`, `otc/`, `tools/*` (re-included
allowlist aside), `OPERATIONS.md`, and `KEYS.txt` are gitignored here; keep it
that way. When in doubt, do not commit — ask.

## Build & test

Go toolchain (pinned): `C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin\go.exe`.
Run from the repo root: `cd C:\Users\Lisa\Desktop\Cereblix\repos\cereblix`.

```sh
# Quick local build of everything / one tool
<go> build ./...
<go> build -o cereblixd       ./cmd/cereblixd
<go> build -o cereblix-wallet ./cmd/cereblix-wallet
<go> build -o cereblix-miner  ./cmd/cereblix-miner
<go> build -o cereblix-stratum ./cmd/cereblix-stratum

# Tests — VM determinism is consensus-critical, must pass:
<go> test ./neuromorph
<go> test ./...
```

**Release node binary (Linux amd64)** — the exact flags used to ship `cereblixd`:

```sh
GOTOOLCHAIN=go1.25.0 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  <go> build -trimpath -ldflags '-s -w' -o cereblixd ./cmd/cereblixd
```

Then sign the manifest with `authority.key` and roll out — see
`.secrets/RELEASE_RUNBOOK.md`. Auto-update version in `upgrade.json` must increase.

**WASM hasher** (browser/phone miner module):

```sh
GOOS=js GOARCH=wasm <go> build -o cereblix.wasm ./cmd/cereblix-wasm
```

**Desktop wallet** (`desktop/`, Wails v2 — Go backend + system webview, vanilla
JS frontend, `replace cereblix => ../`). Build on the target OS (the native
webview can't be cross-compiled):

```sh
cd desktop
wails build                 # -> build/bin/cereblix-wallet.exe (single file)
# Windows pinned path: powershell -ExecutionPolicy Bypass -File build-windows.ps1
# Dev needs:  PATH += toolchain\go\bin + C:\msys64\ucrt64\bin ; CC=ucrt64 gcc ; then `wails dev`
```

**Android wallet** (`wallet-android/`, separate module `cereblix-mobile`,
`replace cereblix => ../`). gomobile AAR, then Gradle:

```sh
cd wallet-android
gomobile bind -target=android -androidapi 21 -o android-app/app/libs/wallet.aar ./mobile
cd android-app && gradlew.bat assembleDebug   # -> app/build/outputs/apk/debug/app-debug.apk
```

The AAR (`./mobile`) reuses `cereblix/core` for keys/signing, so the Android
wallet is crypto-identical to the CLI and desktop wallets. Toolchain: JDK 17,
Android SDK/NDK at `C:\android-build`; gomobile pulls `golang.org/x/mobile` into
**this** module only — the coin module's `go.mod` stays minimal-deps.

## Golden rules

- Code wins over docs. Always re-read the live source + sha256 the running binary
  before acting on consensus/prod/coins.
- NEVER "pkill cereblixd" (use systemctl).
- Commit author is ALWAYS `CereblixCRB <157488947+CereblixCRB@users.noreply.github.com>`,
  NEVER roadtge.
- Consensus strings are FROZEN as `cerebra-*` (historical; do not "fix" them).
- Node auto-update only ever goes FORWARD (version in `upgrade.json` must increase).
- Secrets NEVER leave `.secrets/` (gitignored): `cereblix_core_ed25519`,
  `cereblix_deploy_ed25519`, `authority.key` (signs releases+checkpoints),
  `github_pat.txt`.
- BUILD: Go toolchain at `C:\Users\Lisa\Desktop\Cereblix\toolchain\go\bin\go.exe`;
  node builds use `GOTOOLCHAIN=go1.25.0 CGO_ENABLED=0 GOOS=linux GOARCH=amd64`.
  Desktop wallet: `cd desktop && wails build`. Android: `gomobile bind -> AAR`,
  then `gradle assembleDebug`.

Do not commit or push unless explicitly asked. If on `main`, branch first.

## Secrets

Never write secrets, keys, or tokens to any path outside `.secrets/` (gitignored).
That directory holds `cereblix_core_ed25519`, `cereblix_deploy_ed25519`,
`authority.key`, `github_pat.txt`, `RELEASE_RUNBOOK.md`, `HANDOFF.md`,
`fleet_servers.txt`, `jsverify/`. The push PAT (`ghp_…`) lives in
`.secrets/github_pat.txt` only. `OPERATIONS.md` and `KEYS.txt` are private and
gitignored — never publish them.

## Where to look (reference, do not inline)

- `ARCHITECTURE.md` — full technical spec (NeuroMorph VM, core, node, consensus
  v4, checkpoints, self-update). Authoritative for protocol behavior.
- `README.md` — user-facing build / mine / wallet / node guide.
- `../../INDEX.md` — master map of the working folder: structure, fleet table,
  ops cookbook, secrets locations. Read this for cross-repo / live-system context.
- `.secrets/RELEASE_RUNBOOK.md` — how to build, sign, and roll out a node release
  (contains secrets; never publish).
- Operator-internal architecture, pool-HA, watchtower, mixer, OTC docs live in the
  PRIVATE `cereblix-ops` repo, not here.
