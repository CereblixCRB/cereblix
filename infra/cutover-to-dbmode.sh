#!/bin/bash
# Push-button cutover: prod pool -> FIXED db-mode. Safe: auto-rolls-back to the old pool on ANY error,
# so prod is never left down. Requires the literal arg CONFIRM so it can't run by accident.
#   bash cutover-to-dbmode.sh CONFIRM
set -e
[ "$1" = "CONFIRM" ] || { echo "refusing: run with 'CONFIRM' as the argument"; exit 2; }
TS=$(date +%Y%m%d-%H%M)
BK=/opt/cerebra/backups/cutover-$TS
mkdir -p "$BK"

echo "===== CUTOVER -> fixed db-mode ($TS) ====="
# 0) sanity: fixed binary present
[ -f /opt/cerebra/bin/cereblix-pool.dbmode-fixed ] || { echo "fixed binary missing"; exit 1; }

# 1) backup current prod state
cp -a /var/lib/cerebra/pool.json                              "$BK/pool.json"
cp -a /etc/systemd/system/cereblix-pool.service.d/instr.conf  "$BK/instr.conf"
cp -a /opt/cerebra/bin/cereblix-pool                          "$BK/cereblix-pool.OLD.bin"
echo "backup: $BK"

# auto-rollback if anything below fails (never leave prod down)
rollback_on_error() {
  echo "!!! CUTOVER FAILED — auto-restoring the OLD pool"
  cp "$BK/cereblix-pool.OLD.bin" /opt/cerebra/bin/cereblix-pool
  cp "$BK/instr.conf" /etc/systemd/system/cereblix-pool.service.d/instr.conf
  systemctl daemon-reload; systemctl start cereblix-pool 2>/dev/null || true
  echo "old pool restored: $(systemctl is-active cereblix-pool) sha=$(sha256sum /opt/cerebra/bin/cereblix-pool|cut -c1-12)"
}
trap rollback_on_error ERR

# 2) record source totals
SRC_N=$(python3 -c "import json;print(len(json.load(open('/var/lib/cerebra/pool.json')).get('earned',{})))")
echo "source pool.json: $SRC_N earned miners"

# 3) stop old pool (freezes pool.json — brief downtime starts here)
systemctl stop cereblix-pool
sleep 2

# 4) fresh migrate pool.json -> Postgres cereblix_pool (exits 1 on count mismatch -> trap rolls back)
python3 /opt/cerebra/migrate-pool-state.py --db cereblix_pool

# 5) swap to fixed binary + db-mode drop-in (run-pool-ha.sh: real wallet + cereblix_pool)
cp /opt/cerebra/bin/cereblix-pool.dbmode-fixed /opt/cerebra/bin/cereblix-pool
cat > /etc/systemd/system/cereblix-pool.service.d/instr.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/bin/bash /opt/cerebra/run-pool-ha.sh
EOF
systemctl daemon-reload

# 6) start db-mode pool
systemctl start cereblix-pool
sleep 9
trap - ERR  # past the dangerous window; from here we report, not auto-rollback

# 7) verify
echo "===== VERIFY ====="
echo "running sha: $(sha256sum /opt/cerebra/bin/cereblix-pool|cut -c1-12)  (expect 004d49b657f9)"
echo "pool: $(systemctl is-active cereblix-pool)"
echo -n "health: "; curl -s -m5 http://127.0.0.1:18754/api/health; echo
curl -s http://127.0.0.1:18754/api/poolstats | python3 -c "import sys,json;d=json.load(sys.stdin);print('reconciled: miners=%d owed=%.2f paid=%.2f'%(len(d.get('miners',[])),d.get('total_owed',0)/1e8,d.get('total_paid',0)/1e8))" 2>/dev/null || echo "poolstats not ready yet"
echo
echo "WATCH NOW:  journalctl -u cereblix-pool -f"
echo "  expect within ~60s: 'PAYOUT/cycle1: leader=ok ... planned=N' and payouts sending"
echo "  active_miners rebuilds once over ~60-90s (old pool had no shareEv snapshot) — NORMAL, do NOT restart"
echo "ROLLBACK if needed:  bash /opt/cerebra/rollback-to-oldpool.sh CONFIRM"
