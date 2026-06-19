#!/bin/bash
# Runs the HA pool in db-mode against the LOCAL Postgres. Role (active/standby) is
# auto-derived from whether the local Postgres is primary or a replica — so the SAME
# wrapper runs on both the head and the standby; promoting a node's Postgres activates it.
PW=$(cat /opt/cerebra/pool-db.secret)
CRED=""
[ -f /opt/cerebra/pool-credit.secret ] && CRED="-credit-secret-file /opt/cerebra/pool-credit.secret"
exec /opt/cerebra/bin/cereblix-pool \
  -listen "${POOL_LISTEN:-127.0.0.1:18754}" \
  -node http://127.0.0.1:18751/api \
  -keyfile /opt/cerebra/pool-wallet.txt \
  -fee 1 -shareshift 12 -minpayout 0.05 \
  -state /var/lib/cerebra/pool.json \
  $CRED \
  -db "postgres://pool:$PW@127.0.0.1:5432/cereblix_pool"
