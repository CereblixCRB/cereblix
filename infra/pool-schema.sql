-- Cereblix pool HA state schema (Postgres). Source of truth for money state when
-- the pool runs in -db mode (textbook HA). Applied on the PRIMARY; replicated to standby.
-- Idempotent-ish: run inside a fresh DB (cereblix_pool).

-- per-miner cumulative credited amount (THE source of truth; Owed/Paid derive from chain)
CREATE TABLE IF NOT EXISTS earned (
  addr          TEXT PRIMARY KEY,
  earned_atomic BIGINT NOT NULL DEFAULT 0
);

-- payouts broadcast but not yet confirmed on-chain (prevents double-pay across failover)
CREATE TABLE IF NOT EXISTS inflight (
  txid         TEXT PRIMARY KEY,
  addr         TEXT NOT NULL,
  gross_atomic BIGINT NOT NULL,
  sent_height  BIGINT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- confirmed payout audit log
CREATE TABLE IF NOT EXISTS payouts (
  id            BIGSERIAL PRIMARY KEY,
  txid          TEXT,
  addr          TEXT,
  amount_atomic BIGINT,
  height        BIGINT,
  ts            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- PPLNS sliding window (payout basis). It is kept IN MEMORY by the pool (hot path, one append
-- per accepted share) and snapshotted WHOLE to ONE row here every ~20s + on each block. Writing
-- it row-per-share is the classic pool anti-pattern (MPOS's `shares` table) that collapses
-- throughput under real load; the canonical high-load design keeps the round in a fast store
-- (Redis in open-ethereum-pool) and batches to SQL. The snapshot replicates to the standby, so a
-- promoted node resumes on the same window (≤1 interval of weighting lost on a hard failover —
-- money is `earned`, credited per block, which stays durable).
CREATE TABLE IF NOT EXISTS pplns_snapshot (
  id          SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  window_json JSONB NOT NULL,
  sum_weight  DOUBLE PRECISION NOT NULL DEFAULT 0,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- DEPRECATED (kept only so existing DBs don't error): the old row-per-share window. No longer
-- written or read by the pool — safe to `DROP TABLE pplns;` once no old binary can run.
CREATE TABLE IF NOT EXISTS pplns (
  id     BIGSERIAL PRIMARY KEY,
  addr   TEXT NOT NULL,
  weight DOUBLE PRECISION NOT NULL,
  ts     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- per-miner extranonce map (one JSONB row), snapshotted ~15s + reloaded on startup so a restart /
-- failover hands each miner back the SAME extranonce → in-flight shares are accepted immediately
-- instead of being rejected (notbound) until the next block forces a fresh stratum job.
CREATE TABLE IF NOT EXISTS extranonce_snapshot (
  id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  data       JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- rolling share-stats window (active_miners / hashrate). Loss-tolerant, but snapshotted here (~60s)
-- so a promoted standby shows live stats instead of rebuilding from zero over ~5 min.
CREATE TABLE IF NOT EXISTS shareev_snapshot (
  id         SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  data       JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- single-writer election: only the row-1 lease holder runs the payout loop.
CREATE TABLE IF NOT EXISTS leader (
  id         INT PRIMARY KEY DEFAULT 1,
  holder     TEXT,
  expires_at TIMESTAMPTZ
);
INSERT INTO leader(id, holder, expires_at) VALUES (1, NULL, now())
  ON CONFLICT (id) DO NOTHING;

-- misc key/value (e.g. last reconciled height)
CREATE TABLE IF NOT EXISTS meta (
  k TEXT PRIMARY KEY,
  v TEXT
);
