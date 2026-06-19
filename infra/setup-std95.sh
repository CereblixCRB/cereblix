#!/bin/bash
# Set up the .95 STANDBY pool: canonical binary + chain symlink + listen on the WireGuard IP so the
# head's Caddy can reach it over the mesh. It runs db-mode against the LOCAL Postgres replica, so it
# stays STANDBY (/api/health 503, no shares served) until Patroni promotes pg-95. SAFE: .95 is
# passive — this does NOT touch the head, the prod pool, or any routing.
set -u
NEW=/opt/cerebra/bin/cereblix-pool.canon
EXPECT=ec4d4a43988a
log(){ echo "[$(date +%H:%M:%S)] $*"; }

GOT=$(sha256sum "$NEW" | cut -c1-12); log "uploaded canon sha=$GOT (expect $EXPECT)"
[ "$GOT" = "$EXPECT" ] || { log "SHA MISMATCH - abort"; exit 1; }

# the pool's default -chain is /var/lib/cerebra/blocks.jsonl, but .95's node writes to
# /opt/cereblix/data/blocks.jsonl — symlink so reconcile (and payouts on promotion) can read it.
mkdir -p /var/lib/cerebra
ln -sfn /opt/cereblix/data/blocks.jsonl /var/lib/cerebra/blocks.jsonl
log "chain symlink: $(ls -l /var/lib/cerebra/blocks.jsonl)"

# listen on the WireGuard IP so the head's Caddy can reach this pool over the mesh
mkdir -p /etc/systemd/system/cereblix-pool.service.d
cat > /etc/systemd/system/cereblix-pool.service.d/listen.conf <<'EOF'
[Service]
Environment=POOL_LISTEN=10.10.0.2:18754
EOF
log "drop-in: POOL_LISTEN=10.10.0.2:18754"

# back up + swap the binary (stop -> mv -> start; can't cp over a running exe). Zero prod impact:
# this pool is a passive standby.
TS=$(date +%Y%m%d-%H%M%S); BK=/opt/cerebra/backups/std95-$TS; mkdir -p "$BK"
cp -a /opt/cerebra/bin/cereblix-pool "$BK/PREV.bin" 2>/dev/null
chmod +x "$NEW"; cp -a "$NEW" /opt/cerebra/bin/cereblix-pool.staged
systemctl daemon-reload
systemctl stop cereblix-pool
mv -f /opt/cerebra/bin/cereblix-pool.staged /opt/cerebra/bin/cereblix-pool
systemctl start cereblix-pool; sleep 7
log "pool: active=$(systemctl is-active cereblix-pool) sha=$(sha256sum /opt/cerebra/bin/cereblix-pool|cut -c1-12)"
log "--- startup log ---"
journalctl -u cereblix-pool -n 30 --no-pager | grep -iE 'db-mode ON|STANDBY|node ACTIVE|restored|RECONCILE|cannot read chain' | tail -8
echo -n "health on WG IP (expect 503 standby): "; curl -s -m5 http://10.10.0.2:18754/api/health; echo
log "DONE"
