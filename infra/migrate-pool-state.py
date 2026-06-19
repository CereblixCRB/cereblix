#!/usr/bin/env python3
# Migrate the pool's money state from the live pool.json into Postgres (the Patroni leader),
# at cutover. Migrates `earned` (source of truth) + the `pplns` window (payout basis) — the
# window goes in as ONE pplns_snapshot row (the pool keeps it in memory and snapshots it; it is
# NOT row-per-share). `inflight` is intentionally skipped: the db-mode pool's reconcile() rebuilds
# it from the chain+mempool.
# Run AFTER stopping the old pool so pool.json is frozen (no race). --dry-run = parse only.
import json, sys, psycopg2
from psycopg2.extras import execute_values

dry = "--dry-run" in sys.argv
db = "cereblix_pool"  # target DB; override with: --db cereblix_pool_test (test dry-run)
for i, a in enumerate(sys.argv):
    if a == "--db" and i + 1 < len(sys.argv):
        db = sys.argv[i + 1]
src = "/var/lib/cerebra/pool.json"
pw = open("/opt/cerebra/pool-db.secret").read().strip()
d = json.load(open(src))
earned = d.get("earned", {})
pplns = d.get("pplns", [])
tot = sum(int(v) for v in earned.values())
print("source %s: earned=%d miners (%.2f CRB), pplns=%d entries" % (src, len(earned), tot / 1e8, len(pplns)))

if dry:
    print("DRY-RUN ok (parsed, no DB write)")
    # also test DB connectivity
    try:
        c = psycopg2.connect(host="127.0.0.1", port=5432, dbname=db, user="pool", password=pw, connect_timeout=5)
        cur = c.cursor(); cur.execute("SELECT pg_is_in_recovery()"); rec = cur.fetchone()[0]
        print("DB(%s) connect ok; in_recovery=%s (must be False to write = this is the leader)" % (db, rec))
        c.close()
    except Exception as e:
        print("DB connect FAILED:", e); sys.exit(1)
    sys.exit(0)

print("target DB: %s" % db)
c = psycopg2.connect(host="127.0.0.1", port=5432, dbname=db, user="pool", password=pw, connect_timeout=10)
cur = c.cursor()
# Ensure the snapshot table exists (idempotent; covers an existing DB whose schema predates it).
cur.execute("""CREATE TABLE IF NOT EXISTS pplns_snapshot (
  id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  window_json JSONB NOT NULL, sum_weight DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now())""")
cur.execute("TRUNCATE earned")
execute_values(cur, "INSERT INTO earned(addr,earned_atomic) VALUES %s",
               [(a, int(v)) for a, v in earned.items()])
# Seed the PPLNS window as ONE snapshot row (m/w keys match the pool's pplnsEntry), so the
# db-mode pool resumes paying on the SAME window immediately after cutover (no cold start).
win_json = json.dumps([{"m": e["m"], "w": float(e["w"])} for e in pplns])
win_sum = sum(float(e["w"]) for e in pplns)
cur.execute("""INSERT INTO pplns_snapshot(id, window_json, sum_weight, updated_at)
               VALUES (1, %s::jsonb, %s, now())
               ON CONFLICT (id) DO UPDATE SET window_json=EXCLUDED.window_json,
                 sum_weight=EXCLUDED.sum_weight, updated_at=now()""", (win_json, win_sum))
c.commit()
cur.execute("SELECT count(*), COALESCE(sum(earned_atomic),0) FROM earned")
ec, es = cur.fetchone()
cur.execute("SELECT jsonb_array_length(window_json) FROM pplns_snapshot WHERE id=1")
pc = cur.fetchone()[0]
c.close()
print("MIGRATED -> Postgres: earned rows=%d sum=%.2f CRB, pplns_snapshot entries=%d" % (ec, float(es) / 1e8, pc))
if ec != len(earned) or pc != len(pplns):
    print("!!! COUNT MISMATCH — investigate before proceeding"); sys.exit(1)
print("migration verified: counts match source")
