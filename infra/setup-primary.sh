#!/bin/bash
# Configure the HEAD as the Postgres PRIMARY for pool HA: (re)create role+db+schema,
# a replication role, and streaming-replication settings allowing the standby (.95).
# Idempotent-ish; safe to re-run. Run as root on the head.
set -e
STANDBY_IP="${STANDBY_IP:-13.140.142.95}"
PW=$(cat /opt/cerebra/pool-db.secret)
if [ ! -f /opt/cerebra/pool-repl.secret ]; then
  openssl rand -hex 16 > /opt/cerebra/pool-repl.secret
  chmod 600 /opt/cerebra/pool-repl.secret
fi
RPW=$(cat /opt/cerebra/pool-repl.secret)

sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
DROP DATABASE IF EXISTS cereblix_pool;
DROP ROLE IF EXISTS pool;
DROP ROLE IF EXISTS replicator;
CREATE ROLE pool LOGIN PASSWORD '$PW';
-- C collation: locale-independent btree ordering, so the DB is identical across the
-- head (glibc 2.35) and the standby (glibc 2.39) — no collation-version mismatch.
CREATE DATABASE cereblix_pool OWNER pool TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C';
CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD '$RPW';
SQL

sudo -u postgres psql -d cereblix_pool -v ON_ERROR_STOP=1 -f /opt/cerebra/pool-schema.sql
sudo -u postgres psql -d cereblix_pool -c "ALTER SCHEMA public OWNER TO pool; GRANT ALL ON ALL TABLES IN SCHEMA public TO pool; GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO pool;"

CONF=/etc/postgresql/16/main
if ! grep -q "cereblix-repl" "$CONF/postgresql.conf"; then
  cat >> "$CONF/postgresql.conf" <<EOF

# cereblix-repl
listen_addresses = '*'
wal_level = replica
max_wal_senders = 10
hot_standby = on
EOF
fi
if ! grep -q "replicator $STANDBY_IP" "$CONF/pg_hba.conf"; then
  echo "host replication replicator $STANDBY_IP/32 scram-sha-256" >> "$CONF/pg_hba.conf"
fi
ufw allow from "$STANDBY_IP" to any port 5432 >/dev/null 2>&1 || true
systemctl restart postgresql@16-main
sleep 2
echo "PRIMARY ready (PG16). tables:"
sudo -u postgres psql cereblix_pool -tA -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='public';"
echo "replication role + hba for $STANDBY_IP set; 5432 opened to standby only."
