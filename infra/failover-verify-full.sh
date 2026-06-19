#!/bin/bash
# Verify the COMPLETE seamless failover now that stratum follows the LB too: fail over head->.95,
# confirm BOTH web (/pool) and stratum miners land on .95 (active_miners ~130), then switch back to
# head. Robust: waits for each switchover to finish, waits for the demoted node to become a healthy
# replica before switching back, and always ends on head-primary.
set -u
PC="patronictl -c /etc/patroni/config.yml"
log(){ echo "[$(date +%H:%M:%S)] $*"; }
leader(){ $PC list 2>/dev/null | awk -F'|' '/Leader/{gsub(/ /,"",$2);print $2}'; }
wait_leader(){ for i in $(seq 1 24); do sleep 5; [ "$(leader)" = "$1" ] && { log "  -> leader=$1 (${i}x5s)"; return 0; }; done; log "  TIMEOUT leader!=$1 (now $(leader))"; return 1; }
state(){
  $PC list 2>/dev/null | grep -E 'pg-95|pg-head'
  echo "    head-pool: $(curl -s -m4 http://127.0.0.1:18754/api/health | grep -oE '\"role\":\"[a-z]+\"')   .95-pool: $(curl -s -m4 http://10.10.0.2:18754/api/health | grep -oE '\"role\":\"[a-z]+\"')"
  echo "    public /pool http=$(curl -s -m8 -o /dev/null -w '%{http_code}' https://cereblix.com/pool/api/poolstats)"
}
miners(){ curl -s -m6 "http://$1/api/poolstats" | grep -oE '\"active_miners\":[0-9]+|\"pool_hashrate\":[0-9.]+' | tr '\n' ' '; }

log "===== BEFORE ====="; state; log "head miners: $(miners 127.0.0.1:18754)"

log "===== FAILOVER head -> .95 ====="
$PC switchover cereblix-pg --leader pg-head --candidate pg-95 --force 2>&1 | tail -2
wait_leader pg-95
sleep 30
log "===== AFTER FAILOVER (.95 active; web+stratum both follow) ====="; state
log ".95 miners (should climb ~130 incl. stratum): $(miners 10.10.0.2:18754)"

log "===== wait for pg-head to be a healthy replica ====="
for i in $(seq 1 18); do sleep 5; $PC list 2>/dev/null | grep pg-head | grep -q streaming && { log "  pg-head streaming (${i}x5s)"; break; }; done
sleep 5

log "===== SWITCH BACK .95 -> head ====="
$PC switchover cereblix-pg --leader pg-95 --candidate pg-head --force 2>&1 | tail -2
wait_leader pg-head
sleep 30
log "===== FINAL (head active; miners back) ====="; state
log "head miners: $(miners 127.0.0.1:18754)"
log "DONE-FAILOVER-FULL"
