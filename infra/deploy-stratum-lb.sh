#!/bin/bash
# Make the stratum bridge follow a Postgres failover: add the internal Caddy :18755 LB listener and
# point cereblix-stratum -pool at it (instead of the head loopback pool). Backed up + validated +
# auto-rollback. Normal serving is unchanged (LB -> active head pool); only a failover now reroutes.
set -u
CF=/etc/caddy/Caddyfile; NEW=/tmp/Caddyfile.new
TS=$(date +%Y%m%d-%H%M%S); BK=$CF.bak-$TS
CADDY=$(command -v caddy || echo /usr/bin/caddy)
log(){ echo "[$(date +%H:%M:%S)] $*"; }

cp -a "$CF" "$BK"; log "caddy backup: $BK"
if ! "$CADDY" validate --config "$NEW" --adapter caddyfile >/tmp/cv.txt 2>&1; then log "VALIDATE FAILED:"; tail -6 /tmp/cv.txt; exit 1; fi
cp -a "$NEW" "$CF"
systemctl reload caddy || { log "RELOAD FAILED — restoring"; cp -a "$BK" "$CF"; systemctl reload caddy; exit 1; }
sleep 4
log ":18755 health: $(curl -s -m5 http://127.0.0.1:18755/api/health)"
CODE=$(curl -s -m6 -o /dev/null -w '%{http_code}' "http://127.0.0.1:18755/api/getwork?addr=crb1b1a90fe0fdd522368cc784973c768cf3ca46c9d6")
log ":18755 getwork http=$CODE"
if [ "$CODE" != "200" ]; then log ":18755 NOT serving — ROLLBACK caddy"; cp -a "$BK" "$CF"; systemctl reload caddy; exit 1; fi

# point the pool stratum bridge at the LB endpoint
mkdir -p /etc/systemd/system/cereblix-stratum.service.d
cat > /etc/systemd/system/cereblix-stratum.service.d/lb.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/opt/cerebra/bin/cereblix-stratum -listen :3333 -pool http://127.0.0.1:18755/api
EOF
systemctl daemon-reload
systemctl restart cereblix-stratum
sleep 5
log "stratum: $(systemctl is-active cereblix-stratum) pool=$(systemctl cat cereblix-stratum 2>/dev/null | grep -oE 'http://[^ ]+' | tail -1)"

log "waiting ~40s for stratum miners to reconnect through the LB..."
sleep 40
log "recovered: $(curl -s -m6 http://127.0.0.1:18754/api/poolstats | grep -oE '\"active_miners\":[0-9]+|\"pool_hashrate\":[0-9.]+' | tr '\n' ' ')"
log "DONE"
