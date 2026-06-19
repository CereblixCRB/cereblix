#!/bin/bash
# Configure Patroni on this PG node to manage PostgreSQL 16 with the etcd DCS, over the
# WireGuard mesh. Patroni replaces the systemd-managed PG: it owns start/stop/replication
# and does automatic, quorum-safe failover. Env: PNAME (pg-head/pg-95), SELF_IP (10.10.0.x).
set -e
: "${PNAME:?need PNAME}"; : "${SELF_IP:?need SELF_IP}"
RPW=$(cat /opt/cerebra/pool-repl.secret)
SPW=$(cat /opt/cerebra/pool-super.secret)
DATA=/var/lib/postgresql/16/patroni
PATRONI=$(command -v patroni || echo /usr/local/bin/patroni)

# Hand PG over to Patroni: stop + disable the distro-managed cluster (frees port 5432).
systemctl disable --now postgresql@16-main 2>/dev/null || true
systemctl stop postgresql 2>/dev/null || true

mkdir -p "$DATA" /etc/patroni /var/run/postgresql
chown postgres:postgres "$DATA" /var/run/postgresql
chmod 700 "$DATA"
rm -rf "${DATA:?}"/*   # fresh bootstrap (test data is throwaway)

cat > /etc/patroni/config.yml <<EOF
scope: cereblix-pg
namespace: /cereblix/
name: $PNAME
restapi:
  listen: $SELF_IP:8008
  connect_address: $SELF_IP:8008
etcd3:
  hosts:
    - 10.10.0.1:2379
    - 10.10.0.2:2379
    - 10.10.0.3:2379
bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
    postgresql:
      use_pg_rewind: true
      parameters:
        wal_level: replica
        hot_standby: "on"
        max_wal_senders: 10
        max_replication_slots: 10
        max_connections: 200
  initdb:
    - encoding: UTF8
    - locale: C
    - data-checksums
  pg_hba:
    - local all all trust
    - host all all 127.0.0.1/32 scram-sha-256
    - host all all 10.10.0.0/24 scram-sha-256
    - host replication replicator 10.10.0.0/24 scram-sha-256
postgresql:
  listen: 0.0.0.0:5432
  connect_address: $SELF_IP:5432
  data_dir: $DATA
  bin_dir: /usr/lib/postgresql/16/bin
  pgpass: /tmp/pgpass_patroni
  authentication:
    replication:
      username: replicator
      password: $RPW
    superuser:
      username: postgres
      password: $SPW
  parameters:
    unix_socket_directories: /var/run/postgresql
EOF
chown postgres:postgres /etc/patroni/config.yml
chmod 600 /etc/patroni/config.yml

cat > /etc/systemd/system/patroni.service <<EOF
[Unit]
Description=Patroni PostgreSQL HA
After=network-online.target etcd.service
Wants=etcd.service
[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=$PATRONI /etc/patroni/config.yml
Restart=always
RestartSec=5
KillMode=process
TimeoutSec=60
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
echo "patroni configured: $PNAME @ $SELF_IP (bin=$PATRONI, not started)"
