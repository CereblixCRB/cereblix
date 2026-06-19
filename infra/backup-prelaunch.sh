#!/bin/bash
# Fresh full backup before the test launch of the fixed db-mode pool.
set -e
TS=$(date +%Y%m%d-%H%M)
DIR=/opt/cerebra/backups/$TS
mkdir -p "$DIR"

# 1) live prod state (the OLD pool 999176a4 = current prod + emergency backup)
cp -a /var/lib/cerebra/pool.json                                   "$DIR/pool.json"
cp -a /etc/systemd/system/cereblix-pool.service.d/instr.conf       "$DIR/instr.conf"
cp -a /etc/caddy/Caddyfile                                         "$DIR/Caddyfile"
cp -a /opt/cerebra/bin/cereblix-pool                               "$DIR/cereblix-pool.RUNNING.bin"
cp -a /opt/cerebra/bin/cereblix-pool.dbmode-fixed                  "$DIR/cereblix-pool.dbmode-fixed.bin"

# 2) Postgres earnings (post-cutover ~3168 CRB live only in the DB)
sudo -u postgres pg_dump cereblix_pool > "$DIR/cereblix_pool.sql" 2>/dev/null || echo "WARN: pg_dump failed"

# 3) manifest + restore recipe
{
  echo "Cereblix backup $TS"
  echo "prod pool (LIVE = emergency backup): sha $(sha256sum /opt/cerebra/bin/cereblix-pool | cut -c1-16)  (expect 999176a4c3cc...)"
  echo "fixed db-mode pool (staged):         sha $(sha256sum /opt/cerebra/bin/cereblix-pool.dbmode-fixed | cut -c1-16)  (004d49b657f9...)"
  echo "pool.json size:  $(stat -c%s "$DIR/pool.json") bytes"
  echo "pg dump size:    $(stat -c%s "$DIR/cereblix_pool.sql") bytes"
  echo "earned rows:     $(sudo -u postgres psql cereblix_pool -tAc 'SELECT count(*) FROM earned' 2>/dev/null)"
  echo ""
  echo "RESTORE PROD (old pool) — already running; only if needed:"
  echo "  cp $DIR/cereblix-pool.RUNNING.bin /opt/cerebra/bin/cereblix-pool"
  echo "  cp $DIR/instr.conf /etc/systemd/system/cereblix-pool.service.d/instr.conf"
  echo "  cp $DIR/Caddyfile /etc/caddy/Caddyfile && systemctl reload caddy"
  echo "  cp $DIR/pool.json /var/lib/cerebra/pool.json   # only if accounting must be restored"
  echo "  systemctl daemon-reload && systemctl restart cereblix-pool"
  echo ""
  echo "RESTORE Postgres earnings:  sudo -u postgres psql cereblix_pool < $DIR/cereblix_pool.sql"
} > "$DIR/MANIFEST.txt"

echo "=== backup done: $DIR ==="
cat "$DIR/MANIFEST.txt"
echo "=== files ==="
ls -la "$DIR"
