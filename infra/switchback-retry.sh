#!/bin/bash
# Restore the normal topology: make pg-head the leader again so the head pool is ACTIVE and the
# head's stratum bridge (-> 127.0.0.1:18754) serves the ~100 stratum miners again.
set -u
PC="patronictl -c /etc/patroni/config.yml"
log(){ echo "[$(date +%H:%M:%S)] $*"; }
log "current: $($PC list 2>/dev/null | grep -E 'pg-95|pg-head')"
log "=== switchover -> pg-head ==="
$PC switchover cereblix-pg --leader pg-95 --candidate pg-head --force 2>&1 | tail -3
log "waiting up to 100s for pg-head to become Leader..."
for i in $(seq 1 20); do
  sleep 5
  if $PC list 2>/dev/null | grep pg-head | grep -q Leader; then log "pg-head is Leader after ${i}x5s"; break; fi
done
sleep 8
log "=== verify (head should be active, stratum miners returning) ==="
$PC list 2>/dev/null | grep -E 'pg-95|pg-head'
echo "head-pool: $(curl -s -m4 http://127.0.0.1:18754/api/health)"
echo "public /pool http=$(curl -s -m8 -o /dev/null -w '%{http_code}' https://cereblix.com/pool/api/poolstats)"
echo "head-pool stats: $(curl -s -m6 http://127.0.0.1:18754/api/poolstats | grep -oE '\"active_miners\":[0-9]+|\"pool_hashrate\":[0-9.]+' | tr '\n' ' ')"
log "DONE"
