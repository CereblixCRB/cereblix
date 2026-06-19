#!/bin/bash
# Prepend a current-state header to ROLLBACK.txt so it reflects today's reality.
RB=/opt/cerebra/rollback-prelaunch/ROLLBACK.txt
cat > /tmp/rb-header.txt <<'EOF'
===== CURRENT STATE (updated 2026-06-18 evening) =====
PROD pool   = OLD pool.json pool, sha 999176a4c3cc  (EMERGENCY BACKUP, LIVE + paying). Caddy = direct proxy.
FIXED pool  = /opt/cerebra/bin/cereblix-pool.dbmode-fixed  sha 004d49b657f9  (db-mode, fixed+lab-verified, STAGED, NOT on prod).
TEST DEPLOY = cereblix-pool-test.service :18764 + cereblix-stratum-test.service :3343
              (isolated DB cereblix_pool_test + throwaway wallet /opt/cerebra/test/testwallet.txt). Prod untouched.
FRESH BACKUP + restore recipe: /opt/cerebra/backups/20260618-2040/MANIFEST.txt
DB-mode field guide (symptoms/logs/fixes): repo infra/dbmode-symptoms-logs.md
Post-cutover earnings (~3168 CRB) live in Postgres cereblix_pool (dumped in the backup); reconcile before re-cutover.
(The pre-cutover snapshot manifest below is still valid for the old pool.)
======================================================

EOF
cat /tmp/rb-header.txt "$RB" > /tmp/rb-new.txt && mv /tmp/rb-new.txt "$RB"
rm -f /tmp/rb-header.txt
echo "=== ROLLBACK.txt head ==="
head -14 "$RB"
