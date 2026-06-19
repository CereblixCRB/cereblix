#!/bin/bash
# Persistence test: credit 3 miners -> active_miners=3 -> snapshot -> restart -> active survives.
PW=$(cat /opt/cerebra/pool-db.secret)
echo "labcredsecret123" > /tmp/labcred.secret
SEC=$(cat /tmp/labcred.secret)
DSN="postgres://pool:$PW@127.0.0.1:5432/cereblix_pool_test"

launch() {
  local pid
  pid=$(ss -ltnp 2>/dev/null | grep ":18764" | grep -oP 'pid=\K[0-9]+' | head -1)
  [ -n "$pid" ] && { kill "$pid"; sleep 1; }
  setsid /tmp/poolnew -listen 127.0.0.1:18764 -node http://127.0.0.1:18751/api \
    -keyfile /tmp/labwallet.txt -fee 1 -shareshift 12 -minpayout 0.05 \
    -state /tmp/labpool.json -chain /var/lib/cerebra/blocks.jsonl \
    -credit-secret-file /tmp/labcred.secret \
    -db "$DSN" </dev/null >>/tmp/labpool.log 2>&1 &
  sleep 6
}

a1="crb1$(printf 'a%.0s' {1..38})01"
a2="crb1$(printf 'b%.0s' {1..38})02"
a3="crb1$(printf 'c%.0s' {1..38})03"

# fresh start
pid=$(ss -ltnp 2>/dev/null | grep ":18764" | grep -oP 'pid=\K[0-9]+' | head -1); [ -n "$pid" ] && { kill "$pid"; sleep 1; }
rm -f /tmp/labpool.json /tmp/labpool.json.shareev /tmp/labpool.log
launch

echo "=== inject 3 credited miners ==="
for a in "$a1" "$a2" "$a3"; do
  curl -s -H "X-Credit-Secret: $SEC" "http://127.0.0.1:18764/api/credit?addr=$a&shares=5" -o /dev/null -w "  credit ${a:0:14}…: HTTP %{http_code}\n"
done
sleep 2
echo "=== active_miners BEFORE restart ==="
curl -s http://127.0.0.1:18764/api/poolstats | grep -oE '"active_miners":[0-9]+'

echo "=== wait 32s for the 30s snapshot ==="
sleep 32
echo "snapshot file: $(ls -la /tmp/labpool.json.shareev 2>/dev/null | awk '{print $5" bytes"}')"

echo "=== RESTART ==="
launch
echo "--- restore log ---"
grep "shareEv: restored" /tmp/labpool.log | tail -1
echo "=== active_miners AFTER restart (should still be 3) ==="
curl -s http://127.0.0.1:18764/api/poolstats | grep -oE '"active_miners":[0-9]+'
