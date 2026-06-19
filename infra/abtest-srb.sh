#!/bin/bash
# Drive the test stratum :3343 with real SRBMiner load; the signal is the POOL's STATS.
SRB=$(find /root/srbtest/SRBMiner-Multi-3-3-9 -type f -name "SRBMiner-MULTI" | head -1)
echo "SRBMiner: $SRB"
A=crb1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa01
LABEL="$1"
echo "mining $LABEL via :3343 for ~70s ..."
timeout 85 "$SRB" --algorithm neuromorph --pool 127.0.0.1:3343 --wallet "$A" --disable-gpu --cpu-threads 4 --gpu-auto-tune 0 --disable-cpu-affinity >/tmp/srb-$LABEL.log 2>&1
echo "=== POOL STATS under SRBMiner load ($LABEL) ==="
journalctl -u cereblix-pool-test --no-pager --since "80 sec ago" 2>/dev/null | grep "STATS/2s" | tail -5 | grep -oE "submits=[0-9]+\(\+[0-9]+\)|ACCEPTED=[0-9]+\(\+[0-9]+\)|lowdiff=[0-9]+\(\+[0-9]+\)|notbound=[0-9]+\(\+[0-9]+\)"
echo "=== SRBMiner self-report ==="
grep -iE "accepted|rejected|difficulty|low diff|exception" /tmp/srb-$LABEL.log | tail -10
