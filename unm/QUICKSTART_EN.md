# UNM (Ultra Native Miner) — quick start (CRB / NeuroMorph CPU miner)

Fast CPU miner for **Cereblix (CRB)**. It **auto-tunes to your CPU** — no config
needed. On start it detects your cores, NUMA layout, and the best AES path
(AES‑NI / VAES‑256 / VAES‑512), then mines. One worker per physical core, a
local 64 MiB dataset per NUMA node, fastest fill auto‑selected and byte‑verified.

Works on **any modern Intel or AMD** with **AVX2 + AES‑NI** (Haswell / Zen and
newer — Ryzen, EPYC, Core i/Xeon). VAES is used automatically on Zen3+/Ice Lake+,
VAES‑512 on Zen4/Zen5 and Ice Lake‑SP / Sapphire Rapids.

---

## 1. Get your wallet
A CRB address looks like `crb1` + 40 hex chars. Create one at
https://cereblix.com/wallet/ — you mine straight to it.

## 2. Pick a server (Stratum — recommended)

| Region | Pool | Solo |
|---|---|---|
| 🇩🇪 Europe (default) | `stratum+tcp://stratum.cereblix.com:3333` | `:3334` |
| 🇷🇺 Russia/CIS | `stratum+tcp://ru.cereblix.com:3333` | `:3334` |
| 🇺🇸 USA | `stratum+tcp://us.cereblix.com:3333` | `:3334` |
| 🇸🇬 Asia | `stratum+tcp://asia.cereblix.com:3333` | `:3334` |

(You can also write the bare form `stratum.cereblix.com:3333` — bare `host:port`
means Stratum.)

---

## Windows
1. Download **`unm-windows-amd64.exe`** (static, no install, no DLLs).
2. Open PowerShell/CMD in that folder and run:
```
.\unm-windows-amd64.exe -o stratum+tcp://stratum.cereblix.com:3333 -u crb1YOURADDRESS
```
Auto-start: put that line in a `.bat` file in your Startup folder.

## Linux (any distro)
1. Download **`unm-linux-amd64`** (static, runs anywhere).
```
chmod +x unm-linux-amd64
./unm-linux-amd64 -o stratum+tcp://stratum.cereblix.com:3333 -u crb1YOURADDRESS
```
Run as a service: see the `systemd` snippet in `INSTALL.md`.

## HiveOS
1. Add custom miner: *Flight Sheets → Miners → Add Miner → Custom →* and point
   *Installation URL* at the hosted **`unm-hiveos.tar.gz`** (or unpack it to
   `/hive/miners/custom/`).
2. In the Flight Sheet:
   - **Miner**: `unm`
   - **Wallet/worker template**: your `crb1…` address (`.worker` suffix is fine)
   - **Pool URL**: `stratum+tcp://stratum.cereblix.com:3333`
   - **Extra config args**: usually empty (it auto‑tunes)
3. Apply. Hashrate + accepted shares show on the dashboard.

---

## It just works — but you can override
The miner auto-detects everything. Flags only if you want to tune:
```
-threads N     cap to N workers (default = all physical cores)
-smt           also use Hyper-Threading/SMT siblings (usually a touch slower)
-lanes auto    benchmark nonce-batch lanes (default 1 is best on most CPUs)
-bench 10      offline 10s speed test (no network)
```
Force an AES path for A/B testing (env): `NM_FILL=aesni|vaes256|vaes512`.

## Your own node / bridge / proxy
unm speaks the standard XMRig/Cryptonote Stratum dialect, so point `-o` at
any Stratum endpoint — your self‑hosted **cereblix‑stratum** bridge or a
**cereblix (xmrig) proxy**:
```
./unm-linux-amd64 -o stratum+tcp://YOUR_HOST:3333 -u crb1YOURADDRESS
```
Legacy HTTP getwork still works too (`-o https://cereblix.com/pool/api`), but
Stratum is the default and is more reliable.

## Auto-update (on by default)
The miner checks for a newer **signed** release in the background and updates
itself automatically — download is **SHA-256 verified** before it swaps in (a
`.old` backup is kept), forward-only. You never have to re-download by hand.
```
unm -update      # check & update right now, then exit
unm -noupdate    # disable auto-update (or drop a .noupdate file next to it)
```
Self-hosted update mirror: `NM_UPDATE_URL=https://your-host/ unm ...`

## Verify speed without the network
```
unm -bench 10
```
Prints aggregate H/s, the worker/NUMA plan, and which fill path was chosen.

One CPU, one vote.
