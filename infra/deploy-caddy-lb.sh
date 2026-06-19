#!/bin/bash
# Apply the HA load-balanced /pool Caddy config. Backs up, VALIDATES before applying (a bad config
# is rejected; the live one keeps running), reloads gracefully (zero-downtime), verifies the public
# /pool endpoint, and AUTO-ROLLS-BACK on any problem.
set -u
CF=/etc/caddy/Caddyfile
NEW=/tmp/Caddyfile.new
TS=$(date +%Y%m%d-%H%M%S); BK=$CF.bak-$TS
CADDY=$(command -v caddy || echo /usr/bin/caddy)
log(){ echo "[$(date +%H:%M:%S)] $*"; }
pcode(){ curl -s -m8 -o /dev/null -w "%{http_code}" https://cereblix.com/pool/api/poolstats; }

cp -a "$CF" "$BK"; log "backup: $BK  (caddy=$CADDY)"
log "before: /pool poolstats http=$(pcode)"
if ! "$CADDY" validate --config "$NEW" --adapter caddyfile >/tmp/caddyval.txt 2>&1; then
  log "VALIDATE FAILED — NOT applying:"; tail -6 /tmp/caddyval.txt; exit 1
fi
log "new config validates OK"
cp -a "$NEW" "$CF"
if ! systemctl reload caddy; then
  log "RELOAD FAILED — restoring backup"; cp -a "$BK" "$CF"; systemctl reload caddy; exit 1
fi
sleep 4
log "caddy: $(systemctl is-active caddy)"
log "after: /pool poolstats=$(curl -s -m8 https://cereblix.com/pool/api/poolstats | grep -oE '\"active_miners\":[0-9]+|\"pool_hashrate\":[0-9.]+' | tr '\n' ' ')"
log "after: /pool getwork http=$(curl -s -m8 -o /dev/null -w '%{http_code}' 'https://cereblix.com/pool/api/getwork?addr=crb1b1a90fe0fdd522368cc784973c768cf3ca46c9d6')"
CODE=$(pcode)
if [ "$CODE" != "200" ]; then
  log "POOL ENDPOINT http=$CODE — ROLLING BACK"; cp -a "$BK" "$CF"; systemctl reload caddy; sleep 3
  log "rolled back: /pool http=$(pcode)"; exit 1
fi
log "OK — /pool HA LB live (head active; .95 hot-standby ready). Backup at $BK"
log "DONE"
