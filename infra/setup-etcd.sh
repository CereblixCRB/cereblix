#!/bin/bash
# Configure THIS node as an etcd member of the 3-node cereblix-ha cluster, over the
# WireGuard mesh (10.10.0.0/24). Plaintext etcd is acceptable because the transport is
# the encrypted private WG mesh. Env: ETCD_NAME (node1/2/3), SELF_IP (10.10.0.x).
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq etcd-server etcd-client >/dev/null 2>&1
systemctl stop etcd 2>/dev/null || true
rm -rf /var/lib/etcd/*
cat > /etc/default/etcd <<EOF
ETCD_NAME="$ETCD_NAME"
ETCD_DATA_DIR="/var/lib/etcd"
ETCD_LISTEN_PEER_URLS="http://$SELF_IP:2380"
ETCD_LISTEN_CLIENT_URLS="http://$SELF_IP:2379,http://127.0.0.1:2379"
ETCD_INITIAL_ADVERTISE_PEER_URLS="http://$SELF_IP:2380"
ETCD_ADVERTISE_CLIENT_URLS="http://$SELF_IP:2379"
ETCD_INITIAL_CLUSTER="node1=http://10.10.0.1:2380,node2=http://10.10.0.2:2380,node3=http://10.10.0.3:2380"
ETCD_INITIAL_CLUSTER_STATE="new"
ETCD_INITIAL_CLUSTER_TOKEN="cereblix-ha"
EOF
echo "etcd configured: $ETCD_NAME @ $SELF_IP:2379/2380"
