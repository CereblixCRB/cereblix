# UNM (Ultra Native Miner) — auto-update & release process (operator)

The miner self-updates like the production `cereblix-miner`: it polls a tiny
manifest on two mirrors (GitHub first, then the cereblix.com origin as a
RU-friendly / GitHub-independent fallback), and when a newer version is
published it downloads the platform binary, **verifies its SHA-256 against the
manifest**, atomically swaps it in (keeping a `.old` backup) and re-execs.
Forward-only. Opt out with `-noupdate` or a `.noupdate` file next to the binary.

## What the client looks for
For each mirror base `M` it fetches `M + unm.manifest`:
```json
{ "version": "2.1",
  "notes": "...",
  "linux-amd64":   "<sha256 hex of unm-linux-amd64>",
  "windows-amd64": "<sha256 hex of unm-windows-amd64.exe>" }
```
If `version` > the running `NM_VERSION`, it downloads `M + unm-linux-amd64`
(or `...-windows-amd64.exe`), checks the SHA-256, and installs it.

Mirrors (in order), hardcoded in `unm.c`:
1. `https://github.com/CereblixCRB/cereblix/releases/latest/download/`
2. `https://cereblix.com/`

Override for a self-hosted mirror (your own node): `NM_UPDATE_URL=https://host/`.

## Cutting a new release
1. Bump `NM_VERSION` in `unm.c` (e.g. `2.0` -> `2.1`).
2. Build the portable binaries (runtime-dispatched VAES, runs on any AVX2+AES CPU):
   ```bash
   # Linux (musl static) + Windows, from this folder:
   bash cross-build.sh                                   # -> unm-linux-amd64
   powershell -File build.ps1   # or MSYS2 gcc, static   # -> unm-windows-amd64.exe
   bash package-hiveos.sh                                # -> unm-hiveos.tar.gz
   ```
3. Regenerate the manifest with the new sha256s:
   ```bash
   L=$(sha256sum unm-linux-amd64   | cut -d' ' -f1)
   W=$(sha256sum unm-windows-amd64.exe | cut -d' ' -f1)
   printf '{"version":"2.1","notes":"...","linux-amd64":"%s","windows-amd64":"%s"}\n' "$L" "$W" > unm.manifest
   ```
4. Publish to **both** mirrors so old miners on either path update:
   - GitHub release `latest` assets: `unm.manifest`, `unm-linux-amd64`,
     `unm-windows-amd64.exe` (and `unm-hiveos.tar.gz`).
   - cereblix.com origin: same files at the web root.

That's it — running miners pick it up within ~6h (or immediately with `-update`).

## Bridge & proxy
The **cereblix-stratum bridge** and the **xmrig-cereblix proxy** already carry
their own signed self-update (the proxy announces e.g. "update available v1.2").
Keep them current the same way you do today; unm talks to whatever version
is deployed (it speaks the standard XMRig/Cryptonote Stratum dialect).

## Hardening note (optional next step)
Today the download is SHA-256-verified and fetched over TLS from GitHub /
Cloudflare. To make it tamper-proof even against a compromised mirror, sign the
manifest with the existing **authority key** (the same ed25519 key used for the
node's `UpgradeManifest` / checkpoints) and verify it in the client. The node
already does this in Go (`core/manifest.go`); porting the verify to the C miner
means vendoring a small ed25519 + SHA-512 (e.g. TweetNaCl) — a self-contained
add-on that doesn't change the rest of the flow.
