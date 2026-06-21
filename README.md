# Cereblix (CRB)

**A CPU-only cryptocurrency built from scratch on the self-mutating NeuroMorph
proof-of-work algorithm.** No GPU, no ASIC - ever. One CPU, one vote.

- 🌐 Site & explorer: https://cereblix.com/
- 💼 Web wallet: https://cereblix.com/wallet/
- 🚰 Free faucet: https://cereblix.com/faucet.html
- ⛏️ Mine with UNM: `unm -o stratum+tcp://stratum.cereblix.com:3333 -u crb1...`
- 🇷🇺 RU/CIS Stratum (no Cloudflare): `unm -o stratum+tcp://ru.cereblix.com:3333 -u crb1...`
- 📖 Full design: [ARCHITECTURE.md](ARCHITECTURE.md)

**Community:**
[Telegram](https://t.me/cereblix) ·
[Discord](https://discord.gg/HnffKP86JM) ·
[X / Twitter](https://x.com/Cereblix) ·
[Bitcointalk EN](https://bitcointalk.org/index.php?topic=5585629.0) ·
[Bitcointalk RU](https://bitcointalk.org/index.php?topic=5585637.0) ·
[Altcoinstalks](https://www.altcoinstalks.com/index.php?topic=344237.0)

> A free, open-source project with **zero premine, zero fund, no fundraising**.
> Mine it, fork it, run your own node - the code is all yours.

---

## Why Cereblix

- **🧬 Self-mutating algorithm.** Every 4096 blocks (~2.8 days) NeuroMorph
  rebuilds its own VM semantics from chain entropy - opcode weights, program
  length, constants, AES keys all change. Fixed-function hardware for an
  algorithm that doesn't exist yet is impossible. That is lifelong ASIC
  resistance by construction, not by promise.
- **⚖️ 1 CPU = 1 vote.** Random programs with data-dependent branches starve
  GPUs (warp divergence) - any laptop competes. No farms.
- **🤝 Fair launch.** Empty genesis block, coins exist only from mining.
- **📡 Lightweight node.** One dependency-free Go binary; the chain is
  human-readable JSONL.

## Coin parameters

| | |
|---|---|
| Ticker | CRB (1 CRB = 10⁸ synapses) |
| Algorithm | NeuroMorph v1 - self-mutating PoW VM, CPU-only |
| Block time | 60 s · LWMA difficulty, 90-block window (v2.2.0) |
| Reward | 50 CRB, halving every 1,051,200 blocks (~2 years) |
| Max supply | ~105,120,000 CRB |
| VM mutation epoch | 4096 blocks |
| Premine | **0** |
| Signatures / addresses | ed25519 · `crb1` + SHA-256(pubkey)[:20] |

## Build

**Prebuilt binaries** (node, miner, wallet — Linux/Windows/macOS) are on the
[latest release](https://github.com/CereblixCRB/cereblix/releases/latest).

To build from source — requires Go 1.21+, zero external dependencies (standard
library only):

```sh
git clone https://github.com/CereblixCRB/cereblix.git
cd cereblix
go build ./...

# or build each tool:
go build -o cereblixd        ./cmd/cereblixd
go build -o cereblix-wallet  ./cmd/cereblix-wallet
```

Cross-compile (e.g. Windows from Linux):

```sh
GOOS=windows GOARCH=amd64 go build -o cereblix-wallet.exe ./cmd/cereblix-wallet
```

## Mine

The fastest way to mine is **UNM (Ultra Native Miner)** — our new from-scratch CPU
miner. It **auto-tunes to your CPU** (NUMA nodes, physical cores, best AES path —
AES-NI / VAES-256 / VAES-512) and is the **fastest CRB CPU miner we have measured**:
on a dual **EPYC 7B13** it **beats SRBMiner-Multi 3.3.9** (which takes a 3% fee)
while using fewer threads — with **zero configuration**. It **self-updates**
(signed manifest, SHA-256 verified) and **never drops you** — a built-in
supervisor restarts it on any error and Stratum auto-reconnects.

| Platform | Download (latest release) |
|---|---|
| Windows x64 | [unm-windows-amd64.exe](https://github.com/CereblixCRB/cereblix/releases/latest/download/unm-windows-amd64.exe) |
| Linux x64 | [unm-linux-amd64](https://github.com/CereblixCRB/cereblix/releases/latest/download/unm-linux-amd64) |
| Hive OS | [unm-hiveos.tar.gz](https://github.com/CereblixCRB/cereblix/releases/latest/download/unm-hiveos.tar.gz) |

```sh
# create a wallet first (or use the web wallet: https://cereblix.com/wallet/)
cereblix-wallet new main

# then mine — pool (steady payouts) or solo (whole 50 CRB blocks):
unm -o stratum+tcp://stratum.cereblix.com:3333 -u crb1YOURADDRESS    # pool
unm -o stratum+tcp://stratum.cereblix.com:3334 -u crb1YOURADDRESS    # solo
```

UNM needs **AVX2 + AES-NI** (any Intel Haswell+ / AMD Zen+, incl. EPYC/Ryzen/
Threadripper). It uses one worker per **physical** core automatically — no thread
tuning needed. Regions: `ru.cereblix.com` · `us.cereblix.com` · `asia.cereblix.com`
(append `:3333` pool / `:3334` solo). Offline speed test: `unm -bench 10`. Mine to
**your own node / bridge / proxy** by pointing `-o` at any Stratum host (see below).

> Antivirus software often flags unsigned CPU miners as PUA - add an exclusion
> for the miner file rather than disabling protection.

### Mine in a browser (phone / iOS / Android / desktop)

The NeuroMorph hasher also compiles to WebAssembly, so the coin can be mined in
any browser with no install and no signing - including iOS Safari and Android.
It is much slower than the native miner (a phone does a few to a few dozen H/s)
but runs anywhere. Open `mine.html` on the site, enter your address, start.

Build the wasm module:

```sh
GOOS=js GOARCH=wasm go build -o web/site/cereblix.wasm ./cmd/cereblix-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/site/wasm_exec.js
```

Hashing is verified byte-identical across amd64, arm64 and wasm
(`TestCrossPlatformHash`), so browser/phone-found blocks are accepted.

### Mine in a pool (steady rewards)

Solo mining is a lottery; a pool pays a steady trickle proportional to your work.
Just point UNM at the pool over Stratum:

```sh
unm -o stratum+tcp://stratum.cereblix.com:3333 -u crb1YOURADDRESS
```

On the pool the miner logs `share accepted` - those are *shares* (proofs of work
at an easier target), not full blocks; your real reward arrives as automatic pool
payouts to your address. Each share is cryptographically bound to your address
(per-miner extranonce), so no one can claim your work.

Track everything on the live **pool dashboard** at
[cereblix.com/pool.html](https://cereblix.com/pool.html): enter your address to
see your hashrate, shares, earnings, a **per-worker (rig) breakdown** and a
**daily-payout history with an end-of-day countdown**. The page remembers your
wallet and the URL becomes `?addr=<wallet>`, so you can bookmark it for one-click
access. To see each rig as its own row, give it a name - append `.RIGNAME` to your
address (e.g. `crb1….livingroom`) or pass `--worker NAME` in XMRig/SRBMiner;
unnamed rigs are grouped into one `(default)` row.

**🇷🇺 RU / CIS:** if `cereblix.com` is slow or blocked for you (Cloudflare
throttling), mine through our Moscow relay node instead - same chain, same pool,
same payouts, just a direct route with no Cloudflare in the way:

```sh
unm -o stratum+tcp://ru.cereblix.com:3333 -u crb1YOURADDRESS   # pool
unm -o stratum+tcp://ru.cereblix.com:3334 -u crb1YOURADDRESS   # solo
```

### Mine with XMRig (Stratum — for big multi-core CPUs)

UNM (above) is the fastest option on most CPUs. If you prefer **XMRig**, we ship one too. We ship an **official, fee-free** XMRig build — the
developer donation is removed and the **GPLv3 source is included** — that speaks
our **Stratum** endpoint:

| Platform | Download |
|---|---|
| Windows x64 | https://cereblix.com/xmrig-cereblix-windows-x64.exe |
| Linux x64 | https://cereblix.com/xmrig-cereblix-linux-x64 |
| Hive OS | https://cereblix.com/xmrig-cereblix-hive.tar.gz |
| macOS (Apple Silicon) | https://cereblix.com/xmrig-cereblix-macos-arm64.tar.gz |
| Source (GPLv3) | https://cereblix.com/xmrig-cereblix-src.tar.gz |

Mirror: the [`xmrig` release](https://github.com/CereblixCRB/cereblix/releases/tag/xmrig).
Intel Macs build from source (the macOS binary above is native arm64).

Point it at our public Stratum — **pool** (`:3333`) for steady payouts, **solo**
(`:3334`) for the whole block reward:

```sh
# pool — steady payouts
xmrig-cereblix -o stratum.cereblix.com:3333 -a nm/1 -u crb1YOURADDRESS -p x
# solo — whole 50 CRB blocks
xmrig-cereblix -o stratum.cereblix.com:3334 -a nm/1 -u crb1YOURADDRESS -p x
```

Both endpoints show steady `accepted` shares: the **solo** endpoint uses
**auto-vardiff**, so even a small CPU gets a live hashrate and feedback while real
blocks still pay the full 50 CRB. Pick your own share difficulty with
**`-p diff=50000`** (or login `crb1...+50000`).

**🇷🇺 RU / CIS.** If shares keep dropping out or the connection times out (a flaky
route to the main server), use the Moscow relay instead — a local route, no
Cloudflare, same Stratum protocol. Only the host changes:

```sh
# pool
xmrig-cereblix -o ru.cereblix.com:3333 -a nm/1 -u crb1YOURADDRESS -p x
# solo
xmrig-cereblix -o ru.cereblix.com:3334 -a nm/1 -u crb1YOURADDRESS -p x
```

**CPU cores.** This build uses **all your CPU cores by default**. If it ever
starts on fewer threads than your CPU has, set the count yourself: add
**`-t N`** (N = number of threads), or **`--cpu-max-threads-hint=PERCENT`**
(e.g. `--cpu-max-threads-hint=50` to use half). On **Hive OS** leave *Extra
config arguments* empty - the algorithm is already `nm/1` and all cores are
used; to limit cores there, put `"max-threads-hint": 50` (JSON, not `-t`).

**Squeeze out more hashrate.** Three levers, biggest first:

1. **Huge pages** - often **+20-70%**, the single biggest win. XMRig enables them
   automatically, but you must let the OS hand them over:
   - **Windows:** `secpol.msc` → Local Policies → User Rights Assignment →
     *Lock pages in memory* → add your user, log out/in, then run XMRig **as Administrator**.
   - **Linux:** reserve them once with `sudo sysctl -w vm.nr_hugepages=1280`.
   - The startup log should then read `huge pages 100%`.
2. **Thread count** - "all cores" is not always fastest. NeuroMorph is memory-bound,
   so on **hybrid Intel** (12th-gen+, P+E cores) the P-core count can beat all threads -
   try `-t N` and keep whatever gives more H/s.
3. **Big EPYC / Threadripper** (several CCDs / NUMA nodes) - run **one instance per
   CCD/NUMA node**, each pinned with `--cpu-affinity`, and sum the hashrate; one
   process alone can lag on remote-node memory reads.

With huge pages and the right thread count this fee-free build lands roughly on par
with SRBMiner after its 3% fee. The same huge-pages and per-CCD tips help **SRBMiner** too.

> The **only** official `xmrig-cereblix` is this one (cereblix.com or the `xmrig`
> release). Any other "xmrig-cereblix" you find elsewhere is **not ours** — don't
> run it. To mine with XMRig against *your own* node, see
> [Mine to your own node with XMRig](#mine-to-your-own-node-with-xmrig) below.

### Mine with SRBMiner-Multi (often fastest on big CPUs)

[**SRBMiner-Multi**](https://github.com/doktor83/SRBMiner-Multi) (v3.3.9+) added
native CRB support as the **`neuromorph`** algorithm and is frequently the fastest
option on large CPUs. It is closed-source with a **3% dev fee** (kept by the
SRBMiner team, not us), but the hashrate gain usually more than makes up for it:

```sh
SRBMiner-MULTI --algorithm neuromorph --pool stratum.cereblix.com:3333 --wallet crb1YOURADDRESS --password x
# solo: port :3334   ·   RU/CIS: --pool ru.cereblix.com:3333   ·   name a rig: --worker RIGNAME
```

### Run a farm through the Cereblix Proxy

Running many rigs? Our **fee-free Cereblix Proxy** (a universal Stratum proxy that works with any miner) lets
the whole farm share one connection and one config: point the proxy at the pool,
then point every rig at the proxy.

| Platform | Download |
|---|---|
| Windows x64 | [cereblix-proxy-windows-x64.exe](https://github.com/CereblixCRB/cereblix/releases/latest/download/cereblix-proxy-windows-x64.exe) |
| Linux x64 | [cereblix-proxy-linux-x64](https://github.com/CereblixCRB/cereblix/releases/latest/download/cereblix-proxy-linux-x64) |
| Source (GPLv3) | [xmrig-cereblix-proxy-src.tar.gz](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/xmrig-cereblix-proxy-src.tar.gz) |

```sh
# 1. config.json - "simple" works with any miner; big farms can use "nicehash"
#    ("nicehash" = ONE pool connection for all rigs - needs miner v1.1+ AND proxy v1.2+)
{ "mode": "simple", "pools": [ { "algo": "nm/1", "url": "stratum.cereblix.com:3333", "user": "crb1YOURADDRESS", "pass": "x", "keepalive": true } ], "bind": [ "0.0.0.0:3333" ] }

# 2. run the proxy
cereblix-proxy -c config.json

# 3. point every rig at the proxy (not the pool):
unm -o stratum+tcp://PROXY_IP:3333 -u crb1YOURADDRESS
```

The proxy's upstream must be the **pool** (`stratum.cereblix.com:3333`, or for
RU/CIS `ru.cereblix.com:3333`). Your earnings still track per address on the pool
dashboard.

A ready-made [`config.json`](https://github.com/CereblixCRB/cereblix/releases/latest/download/cereblix-proxy-config.json)
is on the release - just put in your address.

**Monitoring & per-rig stats.** The proxy has a built-in HTTP API. With the
`"http"` block enabled (it is in the sample config) you can read live stats:

```sh
curl http://127.0.0.1:8080/1/summary    # hashrate, accepted, connected miners
curl http://127.0.0.1:8080/1/workers    # per-rig breakdown ("workers": true)
```

The console also prints hashrate and accepted/rejected shares periodically.

**Run it 24/7 (Linux, systemd).** A sample unit
[`cereblix-proxy.service`](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/cereblix-proxy.service)
is on the release:

```sh
sudo useradd -r -s /usr/sbin/nologin cereblix          # once
sudo mkdir -p /opt/cereblix-proxy
sudo cp cereblix-proxy config.json /opt/cereblix-proxy/
sudo cp cereblix-proxy.service /etc/systemd/system/
sudo systemctl enable --now cereblix-proxy
journalctl -u cereblix-proxy -f                        # watch it
```

**Remote rigs over TLS.** To accept rigs over an encrypted connection, add a TLS
bind, e.g. `"bind": ["0.0.0.0:3333", { "host": "0.0.0.0", "port": 3443, "tls": true }]`.

**Updates.** The proxy checks for a newer build on start and prints a one-line
notice with the download link - it **never** downloads or runs anything. Turn it
off with `--no-version-check` or `"version-check": false` in the config.

### Free faucet

No coins yet? Grab a little from the faucet to try the wallet. The anti-bot check
is a real in-browser NeuroMorph **share** (your CPU mines for a moment), so it
doubles as a tiny mining onramp: https://cereblix.com/faucet.html

## Run a full node

```sh
cereblixd -datadir ./data                       # follow the chain
cereblixd -datadir ./data -mine -threads 2 -coinbase crb1YOURADDRESS  # node + miner
```

Your own node's RPC is at `http://127.0.0.1:18751/api`. Point the wallet/miner
at it with `-node http://127.0.0.1:18751/api`.

**Self-updating.** The node keeps itself current automatically: every ~20 min it
fetches an **authority-signed** release manifest (GitHub first, `cereblix.com`
fallback), verifies the SHA-256, swaps the binary and restarts - with automatic
rollback if an update fails to come up healthy, so a bad release can't brick it.
Turn it off per node with `cereblixd -autoupdate off`; check state with
`cereblixd -diagnose`; force a check with `cereblixd -update`. This is how network
upgrades roll out without manual coordination.

**Fees** are a tiny flat anti-spam floor (~0.00001 CRB); under load blocks fill
**highest-fee-first** (pay a bit more to confirm sooner), so the mempool never
stalls. The wallet auto-suggests a fee from current network load.

### Mine to your own node with XMRig

Running your own node and want to mine to it with XMRig — fully trustless, no
public pool or server involved? XMRig speaks **Stratum** while the node speaks
**HTTP getwork**, so a tiny bridge, **`cereblix-stratum`**, sits between them.
Run it locally against your node and every block you find pays 50 CRB straight
to your address, validated by your own node.

Downloads (on the [`xmrig` release](https://github.com/CereblixCRB/cereblix/releases/tag/xmrig)):
[Linux](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/cereblix-stratum-linux-amd64) ·
[Windows](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/cereblix-stratum-windows-amd64.exe) ·
[macOS arm64](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/cereblix-stratum-darwin-arm64) ·
[macOS x64](https://github.com/CereblixCRB/cereblix/releases/download/xmrig/cereblix-stratum-darwin-amd64)
— or build it yourself: `go build ./cmd/cereblix-stratum`.

```sh
# 1. run your node and let it sync
cereblixd -datadir ./data

# 2. bridge Stratum -> your node, with steady feedback shares + auto-vardiff
cereblix-stratum -listen :3334 -pool http://127.0.0.1:18751/api -solo

# 3. point XMRig at your local bridge (uses all cores by default; -t N to set count)
xmrig-cereblix -o 127.0.0.1:3334 -a nm/1 -u crb1YOURADDRESS -p x
```

`-solo` makes the bridge hand XMRig an **easy "feedback" share target with
auto-vardiff** (tuned to your CPU's hashrate and the network), so the miner shows
steady `accepted` shares and a live hashrate instead of looking dead — exactly
like a pool. **Real blocks still go to your node**, which stays the sole authority
on what's a block, so a found block can never be lost. The default difficulty
matches the pool's; pin your own with **`-p diff=50000`** (or login
`crb1...+50000`). Add **`-v`** to the bridge to log every job sent and every share
with its round-trip latency.

The `-pool` flag is just "the getwork HTTP API to mine against": the node RPC
(`…:18751/api`) for trustless **solo** (use `-solo`), or a pool API for **pool**
payouts through your own local Stratum, e.g.
`cereblix-stratum -listen :3333 -pool https://cereblix.com/pool/api` (no `-solo`).

## Standalone CLI wallet

A local key store + RPC client + block explorer, independent of the website
(like `bitcoin-cli`). Keys live only on your machine in `~/.cereblix/wallet.json`
(optionally passphrase-encrypted with PBKDF2 + AES-GCM).

```sh
cereblix-wallet                      # interactive shell
cereblix-wallet new main             # create address
cereblix-wallet list                 # addresses + balances
cereblix-wallet send crb1... 12.5    # sign locally, broadcast
cereblix-wallet encrypt              # passphrase-protect the wallet
cereblix-wallet tx <txid>            # explorer: look up a transaction
cereblix-wallet block 42             # explorer: show a block
cereblix-wallet richlist             # top addresses
```

## Repository layout

```
neuromorph/   NeuroMorph PoW virtual machine
core/         chain, state, mempool, consensus rules, checkpoints
node/         P2P sync, JSON RPC, getwork/submitwork, built-in miner
cmd/          cereblixd · cereblix-miner · cereblix-wallet · cereblix-pool ·
              cereblix-faucet · cereblix-checkpoint · cereblix-wasm
web/          project site + block explorer + web wallet + browser miner
deploy/       systemd unit
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the complete technical specification.

## License

[MIT](LICENSE). Mine it, fork it, mirror it.
