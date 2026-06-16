#!/usr/bin/env bash
# Launch the Cereblix CPU miner. Hive runs this inside a screen session; we tee
# output to the log so stats.sh can report hashrate/shares to the dashboard.
# BASH_SOURCE (not $0) locates us correctly however Hive invokes the script.
cd "$(dirname "${BASH_SOURCE[0]}")"
. h-manifest.conf

# --- keep the binary current from the official mirror -----------------------
# On Hive OS the flight-sheet PACKAGE owns the binary, and Hive re-deploys it on
# restart - so the miner's OWN self-update gets reverted and loops forever. We
# instead refresh the binary HERE, from the official mirrors (GitHub first, then
# cereblix.com for RU/CIS), gated by miner-version.txt so we only download when a
# newer version actually exists. If the network is down we just keep the bundled
# binary. The miner detects Hive and does NOT self-update (no conflict).
MIRRORS="https://github.com/CereblixCRB/cereblix/releases/latest/download https://cereblix.com"
mget() { # mget <outfile> <name> <timeout>; tries each mirror, needs a non-empty file
  local out="$1" name="$2" to="${3:-60}" m
  for m in $MIRRORS; do
    if curl -fsSL --max-time "$to" -o "$out" "$m/$name" && [ -s "$out" ]; then return 0; fi
  done
  return 1
}

if mget /tmp/cereblix-miner-version.txt miner-version.txt 15; then
  LATEST=$(tr -d ' \r\n' < /tmp/cereblix-miner-version.txt)
  HAVE=$(cat .miner-version 2>/dev/null); [ -z "$HAVE" ] && HAVE="$CUSTOM_VERSION"
  if [ -n "$LATEST" ] && [ "$LATEST" != "$HAVE" ]; then
    echo "updating miner $HAVE -> $LATEST from the official mirror..."
    if mget cereblix-miner.new cereblix-miner-linux-amd64 90; then
      mv -f cereblix-miner.new cereblix-miner && echo "$LATEST" > .miner-version
      echo "miner updated to v$LATEST"
    else
      rm -f cereblix-miner.new
      echo "update download failed - keeping the bundled miner"
    fi
  fi
fi
chmod +x cereblix-miner 2>/dev/null

mkdir -p "$(dirname "$CUSTOM_LOG_BASENAME")"

ARGS=$(cat "$CUSTOM_CONFIG_FILENAME" 2>/dev/null)
echo "starting: ./cereblix-miner $ARGS"
# truncating tee (no --append): each (re)start gives stats.sh a fresh log
./cereblix-miner $ARGS 2>&1 | tee "${CUSTOM_LOG_BASENAME}.log"
