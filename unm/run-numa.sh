#!/usr/bin/env bash
# Turnkey NUMA launcher for multi-socket / multi-node EPYC.
#
# The 64 MiB dataset is the hot, latency-bound structure. On a box with >1 NUMA
# node, one shared copy means half the threads pay cross-node memory latency.
# This script runs ONE nmminer process per NUMA node, each bound to that node's
# CPUs and memory (numactl --cpunodebind --membind), so every process builds its
# dataset in LOCAL memory -> no cross-node latency -> higher total hashrate.
# (Same effect as the built-in single process on a 1-node box, just split.)
#
#   ./run-numa.sh -node https://cereblix.com/pool/api -addr crb1YOURADDR -lanes auto
#
# Pass the usual nmminer flags; this script adds the per-node -threads. No root.
set -e
cd "$(dirname "$0")"
[ -x ./nmminer ] || { echo "build first: bash build.sh"; exit 1; }

if ! command -v numactl >/dev/null 2>&1; then
  echo "numactl not found - running a single process across all nodes (interleaved)."
  echo "  (install numactl for per-node binding: apt-get install -y numactl)"
  exec ./nmminer "$@"
fi

NODES=$(numactl -H | awk '/^available:/{print $2}')
if [ -z "$NODES" ] || [ "$NODES" -le 1 ]; then
  echo "single NUMA node -> one process."
  exec ./nmminer "$@"
fi

echo "$NODES NUMA nodes detected -> one nmminer per node (local dataset each)."
pids=""
for ((n=0; n<NODES; n++)); do
  CPUS=$(numactl -H | awk -v pat="^node $n cpus:" '$0 ~ pat {print NF-3}')
  [ -z "$CPUS" ] && CPUS=0
  if [ "$CPUS" -le 0 ]; then echo "  node $n: no cpus, skipping"; continue; fi
  echo "  node $n: $CPUS threads"
  numactl --cpunodebind=$n --membind=$n ./nmminer "$@" -threads "$CPUS" &
  pids="$pids $!"
done

# stop all children together on Ctrl-C
trap 'echo; echo "stopping..."; kill $pids 2>/dev/null; exit 0' INT TERM
wait
