#!/bin/bash
# Dry-run: migrate the CURRENT live pool.json into the TEST DB, then let the fixed pool serve it.
# Zero prod risk: test pool uses a throwaway wallet, so reconcile/payout just LOGS (defers).
set -e
echo "=== current live pool.json totals (source of truth) ==="
python3 -c "import json; d=json.load(open('/var/lib/cerebra/pool.json')); e=d.get('earned',{}); print('  earned miners=%d sum=%.2f CRB pplns=%d'%(len(e), sum(int(v) for v in e.values())/1e8, len(d.get('pplns',[]))))"

echo "=== stop test pool, migrate pool.json -> cereblix_pool_test (sumcheck) ==="
systemctl stop cereblix-pool-test
sleep 2
python3 /opt/cerebra/migrate-pool-state.py --db cereblix_pool_test

echo "=== start test pool on the migrated REAL accounting ==="
systemctl start cereblix-pool-test
sleep 11
echo "--- test pool serving real-scale accounting ---"
curl -s http://127.0.0.1:18764/api/poolstats | python3 -c "import sys,json; d=json.load(sys.stdin); print('  active=%d total_owed=%.2f total_paid=%.2f miners_listed=%d'%(d.get('active_miners',0), d.get('total_owed',0)/1e8, d.get('total_paid',0)/1e8, len(d.get('miners',[]))))"
echo "--- startup + reconcile on real data (no errors expected) ---"
journalctl -u cereblix-pool-test --no-pager --since "13 sec ago" | grep -E "RECONCILE|shareEv: restored|db-mode ON" | tail -3
echo "NOTE: owed looks inflated here ONLY because the test uses a throwaway wallet (reconcile keys on its addr, which never paid). The REAL cutover uses the real wallet -> correct owed."
