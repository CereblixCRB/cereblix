#!/usr/bin/env python3
# Reverse-migration: pull the CURRENT earned + pplns from Postgres back into pool.json,
# so reverting to the old pool.json-mode pool loses NO post-cutover earnings.
import json, shutil, psycopg2
pj = "/var/lib/cerebra/pool.json"
shutil.copy(pj, pj + ".pre-revert")          # safety backup of the frozen file
d = json.load(open(pj))
pw = open("/opt/cerebra/pool-db.secret").read().strip()
c = psycopg2.connect(host="127.0.0.1", port=5432, dbname="cereblix_pool", user="pool", password=pw, connect_timeout=10)
cur = c.cursor()
cur.execute("SELECT addr, earned_atomic FROM earned")
earned = {a: int(v) for a, v in cur.fetchall()}
cur.execute("SELECT addr, weight FROM pplns ORDER BY id")
pplns = [{"m": a, "w": float(w)} for a, w in cur.fetchall()]
c.close()
d["earned"] = earned                          # the live source of truth
d["pplns"] = pplns
d["pplnsSum"] = sum(e["w"] for e in pplns)
json.dump(d, open(pj, "w"))
print("reverse-migrated -> pool.json: earned=%d (%.2f CRB), pplns=%d"
      % (len(earned), sum(earned.values()) / 1e8, len(pplns)))
