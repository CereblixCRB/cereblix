#!/bin/bash
# Verify the test deployment: payout/leader/active logs, then a REAL systemd restart survives.
SEC=$(cat /opt/cerebra/test/testcred.secret)
PW=$(cat /opt/cerebra/pool-db.secret)
echo "=== wait ~68s for PAYOUT cycle1 + ACTIVE lines ==="
sleep 68
echo "--- PAYOUT ---"; journalctl -u cereblix-pool-test --no-pager | grep "PAYOUT/cycle" | tail -2
echo "--- ACTIVE ---"; journalctl -u cereblix-pool-test --no-pager | grep "ACTIVE/10s" | tail -1
echo "--- leader table holder ---"; PGPASSWORD=$PW psql -h 127.0.0.1 -U pool -d cereblix_pool_test -tAc "SELECT holder,(expires_at>now()) AS live FROM leader"

echo "=== inject 3 test miners (credits) ==="
a1="crb1$(printf 'a%.0s' {1..38})01"; a2="crb1$(printf 'd%.0s' {1..38})02"; a3="crb1$(printf 'e%.0s' {1..38})03"
for a in "$a1" "$a2" "$a3"; do
  curl -s -H "X-Credit-Secret: $SEC" "http://127.0.0.1:18764/api/credit?addr=$a&shares=5" -o /dev/null -w "  credit ${a:0:12}…: HTTP %{http_code}\n"
done
sleep 2
echo "active_miners BEFORE restart: $(curl -s http://127.0.0.1:18764/api/poolstats | grep -oE '\"active_miners\":[0-9]+')"

echo "=== wait 32s for snapshot, then a REAL systemctl restart ==="
sleep 32
systemctl restart cereblix-pool-test
sleep 8
echo "--- restore log ---"; journalctl -u cereblix-pool-test --no-pager | grep "shareEv: restored" | tail -1
echo "active_miners AFTER restart: $(curl -s http://127.0.0.1:18764/api/poolstats | grep -oE '\"active_miners\":[0-9]+')"
echo "--- post-restart: db-mode back + leader ---"; journalctl -u cereblix-pool-test --no-pager --since "20 sec ago" | grep -E "db-mode ON|shareEv: restored" | tail -2
