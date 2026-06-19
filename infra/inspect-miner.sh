#!/bin/bash
echo "=== /root/srbtest ==="
ls -la /root/srbtest/ 2>/dev/null
echo "=== json configs ==="
for f in /root/srbtest/*.json; do [ -f "$f" ] && { echo "--- $f ---"; head -60 "$f"; }; done
echo "=== connection params from logs ==="
grep -iE "algo|3343|3333|neuromorph|login|accepted|rejected|pool" /root/srbtest/srb.log 2>/dev/null | head -12
echo "=== xmrig-test help (algos/flags) ==="
/root/srbtest/xmrig-test --help 2>&1 | grep -iE "algo|--url|--user|--coin|--threads|--pass" | head
