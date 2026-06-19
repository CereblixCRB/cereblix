#!/bin/bash
# Stand up the persistent TEST deployment of the FIXED db-mode pool (isolated; prod untouched).
set -e
mkdir -p /opt/cerebra/test
PW=$(cat /opt/cerebra/pool-db.secret)

# 1) throwaway test wallet (isolated economy — never the real pool wallet)
if [ ! -f /opt/cerebra/test/testwallet.txt ]; then
  python3 -c "from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey as K; from cryptography.hazmat.primitives import serialization as S; k=K.generate(); s=k.private_bytes(S.Encoding.Raw,S.PrivateFormat.Raw,S.NoEncryption()); p=k.public_key().public_bytes(S.Encoding.Raw,S.PublicFormat.Raw); print('PRIVATE KEY '+(s+p).hex())" > /opt/cerebra/test/testwallet.txt
fi
chmod 600 /opt/cerebra/test/testwallet.txt
[ -f /opt/cerebra/test/testcred.secret ] || echo "testcred-$(date +%s)" > /opt/cerebra/test/testcred.secret
chmod 700 /opt/cerebra/run-pool-test.sh

# 2) isolated test DB + schema
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='cereblix_pool_test'" | grep -q 1 || \
  sudo -u postgres psql -tAc "CREATE DATABASE cereblix_pool_test OWNER pool TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'"
PGPASSWORD=$PW psql -h 127.0.0.1 -U pool -d cereblix_pool_test -f /opt/cerebra/pool-schema.sql >/dev/null 2>&1
echo "test DB ready (leader rows: $(PGPASSWORD=$PW psql -h 127.0.0.1 -U pool -d cereblix_pool_test -tAc 'SELECT count(*) FROM leader'))"

# 3) test pool service (uses the wrapper -> no password in the unit)
cat > /etc/systemd/system/cereblix-pool-test.service <<'EOF'
[Unit]
Description=Cereblix TEST pool (FIXED db-mode) :18764
After=network.target
[Service]
ExecStart=/bin/bash /opt/cerebra/run-pool-test.sh
Restart=always
RestartSec=2
[Install]
WantedBy=multi-user.target
EOF

# 4) test stratum service :3343 -> test pool
cat > /etc/systemd/system/cereblix-stratum-test.service <<'EOF'
[Unit]
Description=Cereblix TEST stratum :3343 -> test pool :18764
After=network.target cereblix-pool-test.service
[Service]
ExecStart=/opt/cerebra/bin/cereblix-stratum -listen :3343 -pool http://127.0.0.1:18764/api
Restart=always
RestartSec=2
[Install]
WantedBy=multi-user.target
EOF

ufw allow 3343/tcp >/dev/null 2>&1 || true

systemctl daemon-reload
systemctl enable --now cereblix-pool-test.service >/dev/null 2>&1
sleep 5
systemctl enable --now cereblix-stratum-test.service >/dev/null 2>&1
sleep 3

echo "=== test pool: $(systemctl is-active cereblix-pool-test) ==="; ss -ltn | grep 18764 || echo "  NOT listening"
echo "=== test stratum: $(systemctl is-active cereblix-stratum-test) ==="; ss -ltn | grep 3343 || echo "  NOT listening"
echo "=== test pool startup logs ==="; journalctl -u cereblix-pool-test --no-pager | grep -E "db-mode|RECONCILE|addr |INSTRUMENTED|shareEv" | tail -6
echo "=== test wallet addr (isolated economy) ==="; curl -s http://127.0.0.1:18764/api/poolstats | grep -oE '"pool_address":"[^"]+"'
