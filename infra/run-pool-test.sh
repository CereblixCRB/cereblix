#!/bin/bash
# TEST pool launcher (fixed db-mode binary, isolated test DB + throwaway wallet).
# Reads the DB secret at runtime so the password never lands in the systemd unit file.
PW=$(cat /opt/cerebra/pool-db.secret)
exec /opt/cerebra/bin/cereblix-pool.dbmode-fixed \
  -listen 127.0.0.1:18764 \
  -node http://127.0.0.1:18751/api \
  -keyfile /opt/cerebra/test/testwallet.txt \
  -fee 1 -shareshift 12 -minpayout 0.05 \
  -state /opt/cerebra/test/testpool.json \
  -chain /var/lib/cerebra/blocks.jsonl \
  -credit-secret-file /opt/cerebra/test/testcred.secret \
  -db "postgres://pool:$PW@127.0.0.1:5432/cereblix_pool_test"
