#!/bin/bash
# End-to-end smoke test of the standalone CLI wallet against the local node.
set -e
export PATH=$PATH:/usr/local/go/bin
cd /opt/cerebra/src
go build -o /tmp/cwallet ./cmd/cerebra-wallet
go build -o /tmp/cwallet-lin ./cmd/cerebra-wallet   # ensure builds

API=http://127.0.0.1:18751/api
rm -rf /tmp/wtest && mkdir -p /tmp/wtest
W="/tmp/cwallet -wallet /tmp/wtest/wallet.json -node $API"

echo "== create two addresses =="
$W new main
$W new savings
echo "== list (one-shot) =="
$W list
echo "== explorer: status =="
$W status
echo "== explorer: latest 3 =="
$W latest 3
echo "== explorer: block 10 =="
$W block 10
echo "== explorer: richlist 3 =="
$W richlist 3
echo "== explorer: address (network wallet) =="
$W address crb110047ab2e7fd8cc04b8484f58a87e29fcb97c857 | head -8

echo "== encryption round-trip =="
CEREBRA_PASSPHRASE=example-passphrase $W encrypt
echo "-- list after encrypt (needs passphrase) --"
CEREBRA_PASSPHRASE=example-passphrase $W list
echo "-- wrong passphrase should fail --"
CEREBRA_PASSPHRASE=wrongpass $W list 2>&1 | head -1 || true

echo "== wallet file is encrypted on disk (no plaintext priv) =="
if grep -q '"priv"' /tmp/wtest/wallet.json; then echo "FAIL: plaintext key on disk"; else echo "OK: no plaintext keys in file"; fi
cat /tmp/wtest/wallet.json | python3 -c 'import sys,json; d=json.load(sys.stdin); print("encrypted=",d["encrypted"],"kdf=",d.get("kdf"))'

echo "== DONE =="
