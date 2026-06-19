#!/bin/bash
# Emergency rollback: db-mode pool -> the OLD pool.json pool (999176a4), the proven backup.
#   bash rollback-to-oldpool.sh CONFIRM
# NOTE: earnings credited DURING the db-mode period live in Postgres cereblix_pool, NOT pool.json.
# After rolling back, recover them with:  python3 /opt/cerebra/revert-to-pooljson.py   (reverse-migrate)
set -e
[ "$1" = "CONFIRM" ] || { echo "refusing: run with 'CONFIRM' as the argument"; exit 2; }

OLD=/opt/cerebra/bin/cereblix-pool.ROLLBACK-prelaunch.bak   # 999176a4 (pool.json mode)
DROPIN=/opt/cerebra/rollback-prelaunch/instr.conf           # old-pool ExecStart
[ -f "$OLD" ] || { echo "old binary backup missing: $OLD"; exit 1; }

echo "===== ROLLBACK -> old pool.json pool ====="
cp "$OLD" /opt/cerebra/bin/cereblix-pool
cp "$DROPIN" /etc/systemd/system/cereblix-pool.service.d/instr.conf
systemctl daemon-reload
systemctl restart cereblix-pool
sleep 6
echo "running sha: $(sha256sum /opt/cerebra/bin/cereblix-pool|cut -c1-12)  (expect 999176a4c3cc)"
echo "pool: $(systemctl is-active cereblix-pool)"
curl -s http://127.0.0.1:18754/api/poolstats | python3 -c "import sys,json;d=json.load(sys.stdin);print('active=%d owed=%.2f paid=%.2f'%(d.get('active_miners',0),d.get('total_owed',0)/1e8,d.get('total_paid',0)/1e8))" 2>/dev/null || true
echo "REMINDER: to recover db-mode-period earnings -> python3 /opt/cerebra/revert-to-pooljson.py"
