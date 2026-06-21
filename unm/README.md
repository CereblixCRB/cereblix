# nmminer — fast NeuroMorph CPU miner for Cereblix (CRB)

A from-scratch, high-performance CPU miner for the **NeuroMorph** proof-of-work.
Byte-identical hashes to consensus, **~1.4–2.1× the throughput of the current
production `xmrig-cereblix` fork**, and it **adapts to each epoch and to the host
CPU** (it can benchmark itself and pick the fastest configuration). Built to
saturate many-core servers (EPYC, 128 threads).

```
solo:  ./nmminer -node http://YOUR_NODE_IP:18751/api -addr crb1xxxx...   # all cores, auto-tuned
tune:  ./nmminer -node ... -addr crb1... -lanes auto                     # pick the best lane count for this CPU
bench: ./nmminer -bench 10 -threads 128 -lanes 1                         # offline throughput test, no network
```

---

## Why it's faster — we profiled first

NeuroMorph (post dataset-activation) spends its cycles like this **per hash**
(measured, `prog_size≈564`, `loops≈59`):

| phase | what it does | share of a hash |
|---|---|---:|
| `fill_scratch` | fill the 2 MiB scratchpad with AES-CTR | **49%** |
| `dataset_walk` | 64 dependent random reads/loop over the 64 MiB dataset | **24%** |
| `vm_interp` | run the per-nonce register VM | 9% |
| `final_fold` | XOR-fold the whole scratchpad | 4% |
| seed/program/sha | setup + final hash | <1% |

The cost is **memory, not the VM.** JIT-compiling the interpreter (what most
"faster miner" attempts reach for) would only attack the 9% slice. The two real
levers are the memory phases — and that's exactly what nmminer optimizes, while
producing the **identical** consensus hash (verified against the reference
vector `a638d54f…bc7a`).

### Win #1 — a fill_scratch that doesn't fight the byte order (the big one)

The reference fills the scratchpad through scalar `le64`/`put64` helpers that
shuffle each 8-byte word a bit at a time. On a little-endian CPU that shuffle is
pure overhead. nmminer keeps each AES result in an SSE register and stores it
**directly** (`_mm_storeu_si128`) with an inlined 8-wide AES-NI pipeline.

```
fill_scratch:  1,376,049 → 430,106 cycles   (×3.2 on that phase)
whole hash:    2,783,462 → 1,847,584 cycles  (×1.5)   — same bytes out
```

### Win #2 — batch lanes to hide the 64 MiB latency wall

`dataset_walk` is a *dependent* read chain: each address depends on the value
just read, so a single hash stalls on DRAM latency and can't prefetch. But
`prog_size`/`loops` are fixed **per epoch** — identical for every nonce — so K
nonces march in perfect lockstep. nmminer runs them as **K interleaved lanes**
and issues the K dataset reads back-to-back: K independent DRAM accesses in
flight, latency hidden (the same memory-level-parallelism trick RandomX uses).

This is a **tunable** win, because K lanes need K×2 MiB of cache:
- big last-level cache / latency-bound memory (servers, EPYC) → K=2–3 helps;
- cache-starved desktops under full load → K=1 is best (extra lanes thrash LLC).

So nmminer defaults to **K=1** (which never loses to the reference) and offers
**`-lanes auto`** to measure 1..4 on your machine and pick the winner.

---

## Proven results (Intel i5-1235U, same harness, steady-state, pinned)

Head-to-head vs the production `xmrig-cereblix` computed-goto core
(`nmref_mt` runs that exact core under nmminer's threading harness):

| | reference fork | **nmminer** | speedup |
|---|---:|---:|---:|
| 1 thread | 792 H/s | **1219 H/s** | **×1.54** |
| 12 threads (full load) | 2185 H/s | **3048 H/s** | **×1.40** |
| 1 core, batch K=3 (cache free) | — | 1870 H/s | ×2.08 vs ref |

Reproduce:

```
make tools
./nmref_mt 10 12       # reference core, 12 threads
./nmminer -bench 10 -threads 12 -lanes auto
```

On EPYC the gap should widen: far more L3 per core (Zen4 ~4 MB/core, Zen4-X
V-cache ~12 MB/core) lets the batch lanes pay off, and remote-NUMA dataset
latency — the exact thing lane-interleaving hides — is worse there than on a
single-socket desktop.

---

## Build

**Linux / EPYC** (build *on* the target so `-march=native` lights up Zen4/Zen5):

```
make                       # -> ./nmminer
make tools                 # -> ./nmbench_fast (correctness+bench), ./nmref_mt (A/B vs reference)
make ARCH=x86-64-v3        # portable binary (any Haswell/Zen+; needs AES-NI + AVX2)
```

**Windows** (MSYS2 ucrt64 gcc):

```
powershell -ExecutionPolicy Bypass -File build.ps1
```

Requires a CPU with **AES-NI + AVX2** (any Zen or Haswell-and-newer). The build
uses `-fno-tree-vectorize -ffp-contract=off` so the consensus-critical IEEE-754
float path is never reordered or fused — output stays byte-identical.

---

## Running on a big EPYC box

```
# 1) (recommended) reserve 2 MiB huge pages for the dataset + scratchpads.
#    Need ~ (64 MiB dataset + threads*lanes*2 MiB). For 128 threads, K=2: ~512 MiB.
sudo sysctl vm.nr_hugepages=320          # 320 * 2 MiB = 640 MiB

# 2) spread the shared 64 MiB dataset across all memory controllers so no single
#    NUMA node becomes the latency hotspot, then let nmminer pin one thread/core:
numactl --interleave=all \
  ./nmminer -node http://127.0.0.1:18751/api -addr crb1youraddr -lanes auto
```

- **Threads:** default = all logical CPUs. Override with `-threads N`.
- **Lanes:** `-lanes auto` benchmarks 1..4 at your thread count and picks the
  best (≈12 s). Or pin it: `-lanes 1|2|3`. Start with `auto`.
- **Huge pages:** allocated automatically when available (`MAP_HUGETLB`), with a
  transparent-huge-page (`MADV_HUGEPAGE`) and then a plain-aligned fallback — it
  always runs, just faster with huge pages reserved.
- **NUMA:** v1 shares one 64 MiB dataset; `numactl --interleave=all` averages the
  access latency across nodes. (Per-node dataset replication is the next step —
  see *Roadmap*.)

---

## How it adapts per epoch

Every 4096 blocks NeuroMorph re-derives its VM semantics (opcode table, program
length, loop count, AES key, and the 64 MiB dataset) from chain entropy, so
fixed-function hardware can't be pre-built. nmminer follows along automatically:
the work-fetch loop watches the `seed` field from `getwork`, and when it changes
it re-derives the params and **rebuilds the 64 MiB dataset** (shared read-only
across all threads) before publishing the new work — miners never compute against
a stale epoch. Older datasets are retained one deep so in-flight hashes stay
valid across the switch.

---

## Protocol

Same HTTP getwork as `cereblix-miner`:
- `GET  {node}/getwork?addr=ADDR` → `{id, header(hex), target(hex), seed(hex), epoch, height, extranonce}`
- `POST {node}/submitwork` ← `{"id":..., "nonce":"..."}`

Nonce layout matches the native miner: `[extranonce:16][thread:8][counter:40]`,
embedded little-endian at byte offset 116 of the 124-byte header.

**Same endpoints as the native miner.** `-node` takes the same URLs:
`https://cereblix.com/pool/api` (pool), `https://cereblix.com/api` (solo), or
`http://NODE_IP:18751/api` (direct node). `https://` is handled by shelling out to
**curl** (TLS/Cloudflare/redirects), which ships with Windows 10+/Linux/HiveOS —
no linked libssl, so none of the HiveOS libssl-crash risk. `http://` uses raw
sockets directly.

---

## Files

| file | role |
|---|---|
| `nm_fast.{h,c}` | the optimized core: `nm_fast_hash`, `nm_fast_hash_batch`, epoch/lane/dataset management |
| `nmminer.c` | the miner: threads + pinning, getwork client, per-epoch dataset rebuild, `-bench`, `-lanes auto` |
| `core_addr.h` | `crb1`+40-hex address check (mirrors `core.ValidAddr`) |
| `nm_aes.h` `nm_sha256.h` `nm_params.{c,h}` | reused verbatim from the fork → guarantees identical AES/SHA/param derivation |
| `nmbench_fast.c` | correctness (VECTOR + batch==single) and per-lane benchmark |
| `nmref_mt.c` | reference core under the same harness, for the honest A/B above |
| `nmprofile*.c` | the per-phase cycle profilers used to find the bottlenecks |
| `mock_node.py` | a tiny local getwork/submit stub for end-to-end testing |

---

## Fleet-wide CPU adaptation (done)

- **Auto-selected scratch fill path.** At startup the miner benchmarks AES-NI vs
  **VAES-256** vs **VAES-512** and picks the fastest that *reproduces the AES-NI
  bytes* — so VAES-512 is used on Zen4/Zen5 (Genoa/Turin, Ryzen 7000/9000) where it
  wins, VAES-256 on Zen3/modern Intel, AES-NI on Zen2 (Rome/Ryzen 3000), with no
  per-arch guesswork and no risk (a wrong/slow path is auto-rejected by the byte
  check + the timing). One binary, runtime CPUID dispatch. Override: env
  `NM_FILL=aesni|vaes256|vaes512`, `NM_NO_VAES=1`.
- **Multi-socket NUMA**: `run-numa.sh` launches one process per NUMA node bound to
  local CPUs+memory, so each builds its 64 MiB dataset in local DRAM (kills the
  cross-node latency on the dataset walk — the biggest dual-socket EPYC win).
- **Lane autotune** over {1,2,3,4,6,8,12,16} sized to the host's cache.

## Roadmap (further speed, not yet done)

- **Built-in (in-process) per-NUMA dataset replication** (today done via
  `run-numa.sh` running one process per node — same effect, simpler ops).
- **Persistent submit connection** for pool mining with frequent shares.
- **Built-in TLS** (link a small lib like mbedTLS) to drop the curl dependency for
  https endpoints (today https works via curl, which is fine on all targets).

One CPU, one vote.
