#!/bin/bash
# Make THIS box (.95) a streaming Postgres REPLICA of the head primary.
# Wipes the local PG16 data dir and rebuilds it from the primary via pg_basebackup.
# Requires /opt/cerebra/pool-repl.secret (the replicator role's password) present locally.
# Run as root on the standby.
set -e
PRIMARY_IP="${PRIMARY_IP:-188.34.181.191}"
RPW=$(cat /opt/cerebra/pool-repl.secret)
DATA=/var/lib/postgresql/16/main

systemctl stop postgresql@16-main
rm -rf "$DATA"
sudo -u postgres bash -c "PGPASSWORD='$RPW' pg_basebackup -h $PRIMARY_IP -p 5432 -U replicator -D '$DATA' -Fp -Xs -P -R -d 'sslmode=require'"

# password for ongoing streaming (restarts): libpq reads ~postgres/.pgpass
PGPASS=/var/lib/postgresql/.pgpass
echo "$PRIMARY_IP:5432:replication:replicator:$RPW" > "$PGPASS"
chown postgres:postgres "$PGPASS"
chmod 600 "$PGPASS"

systemctl start postgresql@16-main
sleep 4
echo "in_recovery (expect t = replica):"
sudo -u postgres psql -tA -c "SELECT pg_is_in_recovery();"
echo "last received WAL:"
sudo -u postgres psql -tA -c "SELECT pg_last_wal_receive_lsn();"
