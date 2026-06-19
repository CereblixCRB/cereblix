#!/bin/bash
# Apply the pool role + cereblix_pool DB + schema on the Patroni LEADER (run on head only;
# it replicates to the standby automatically). Patroni already created postgres + replicator.
set -e
PW=$(cat /opt/cerebra/pool-db.secret)
sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
DROP DATABASE IF EXISTS cereblix_pool;
DROP ROLE IF EXISTS pool;
CREATE ROLE pool LOGIN PASSWORD '$PW';
CREATE DATABASE cereblix_pool OWNER pool TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C';
SQL
sudo -u postgres psql -d cereblix_pool -v ON_ERROR_STOP=1 -f /opt/cerebra/pool-schema.sql
sudo -u postgres psql -d cereblix_pool -c "ALTER SCHEMA public OWNER TO pool; GRANT ALL ON ALL TABLES IN SCHEMA public TO pool; GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO pool;"
echo "schema applied on Patroni leader: $(sudo -u postgres psql -d cereblix_pool -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'") tables"
