#!/usr/bin/env bash
# Launch UNM (Ultra Native Miner) under Hive OS. Hive runs this inside a screen
# session; we tee output to the log so stats.sh can report hashrate/shares.
cd "$(dirname "${BASH_SOURCE[0]}")"
. h-manifest.conf

# --- ensure a runnable binary ----------------------------------------------
# Prefer the bundled prebuilt ./unm. If it is missing or won't run on this CPU,
# build it from the bundled source (best perf: -march=native on the rig).
need_build=0
if [ ! -x ./unm ]; then
  need_build=1
elif ! ./unm -h >/dev/null 2>&1; then
  echo "bundled unm won't run here (CPU/libs) - rebuilding from source"; need_build=1
fi
if [ "$need_build" = 1 ]; then
  if [ -f ./build.sh ]; then
    echo "building UNM for this rig..."
    bash ./build.sh && mv -f nmminer unm 2>/dev/null
    [ -x ./unm ] || { echo "BUILD FAILED - install gcc (apt-get install -y gcc) and retry"; exit 1; }
  else
    echo "ERROR: no ./unm binary and no build.sh to build it. Bundle one of them."; exit 1
  fi
fi
chmod +x ./unm 2>/dev/null

mkdir -p "$(dirname "$CUSTOM_LOG_BASENAME")"

ARGS=$(cat "$CUSTOM_CONFIG_FILENAME" 2>/dev/null)
echo "starting: ./unm $ARGS"
# truncating tee (no --append): each (re)start gives stats.sh a fresh log
./unm $ARGS 2>&1 | tee "${CUSTOM_LOG_BASENAME}.log"
