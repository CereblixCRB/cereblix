#!/bin/bash
# Launch the fixed db-mode pool in the isolated lab (test DB + throwaway wallet).
PW=$(cat /opt/cerebra/pool-db.secret)
pid=$(ss -ltnp 2>/dev/null | grep ":18764" | grep -oP 'pid=\K[0-9]+' | head -1)
[ -n "$pid" ] && { kill "$pid"; sleep 1; }
rm -f /tmp/labpool.json /tmp/labpool.json.shareev /tmp/labpool.log
setsid /tmp/poolnew -listen 127.0.0.1:18764 -node http://127.0.0.1:18751/api \
  -keyfile /tmp/labwallet.txt -fee 1 -shareshift 12 -minpayout 0.05 \
  -state /tmp/labpool.json -chain /var/lib/cerebra/blocks.jsonl \
  -db "postgres://pool:$PW@127.0.0.1:5432/cereblix_pool_test" </dev/null >/tmp/labpool.log 2>&1 &
sleep 7
echo "=== startup log ==="
cat /tmp/labpool.log
echo "=== listening on :18764? ==="
ss -ltn | grep :18764 || echo NO
