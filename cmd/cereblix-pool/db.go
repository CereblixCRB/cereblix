// db.go — Postgres + Redis state backend for HA mode (enabled with -db).
//
// When -db is set, the pool's MONEY state lives in shared, replicated storage instead of
// the local pool.json, so a standby instance can take over the SAME accounting on failover:
//   - earned   (cumulative credited per address) — the source of truth
//   - pplns_snapshot (sliding payout window, snapshotted WHOLE every ~20s — NOT row-per-share)
//   - inflight  (broadcast-but-unconfirmed payouts → no double-pay across failover)
//   - payouts   (confirmed audit log)
//   - leader    (single-writer lease → only one instance runs the payout loop)
// Hot, loss-tolerant display counters can use Redis.
//
// Without -db the pool behaves exactly as before (pool.json); this file is additive.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	pg         *pgxpool.Pool
	rdb        *redis.Client
	dbBG       = context.Background()
	dbMode     bool
	instanceID string // unique id of this pool process (leader election + logs)
)

// dbInit connects to Postgres (required) and Redis (optional). Sets dbMode on success.
func dbInit(pgDSN, redisAddr string) error {
	cfg, err := pgxpool.ParseConfig(pgDSN)
	if err != nil {
		return fmt.Errorf("pg dsn: %w", err)
	}
	cfg.MaxConns = 16
	p, err := pgxpool.NewWithConfig(dbBG, cfg)
	if err != nil {
		return fmt.Errorf("pg connect: %w", err)
	}
	ctx, cancel := context.WithTimeout(dbBG, 5*time.Second)
	defer cancel()
	if err := p.Ping(ctx); err != nil {
		return fmt.Errorf("pg ping: %w", err)
	}
	pg = p
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("redis ping: %w", err)
		}
	}
	dbMode = true
	return nil
}

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(dbBG, 5*time.Second)
}

// dbInRecovery reports whether this node's Postgres is a replica (read-only). The pool
// derives its active/standby role from this: primary (writable) → active; replica → standby.
// So promoting the replica's Postgres automatically activates that node's pool.
func dbInRecovery() (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var rec bool
	err := pg.QueryRow(ctx, `SELECT pg_is_in_recovery()`).Scan(&rec)
	return rec, err
}

// ----- earned (source of truth) -----

// dbEarn adds atomic units to one miner's cumulative earned.
func dbEarn(addr string, atomic uint64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO earned(addr,earned_atomic) VALUES($1,$2)
		 ON CONFLICT(addr) DO UPDATE SET earned_atomic = earned.earned_atomic + EXCLUDED.earned_atomic`,
		addr, int64(atomic))
	return err
}

// dbCreditBlock atomically credits a whole block's per-miner payouts (one transaction).
func dbCreditBlock(credits map[string]uint64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := pg.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for addr, amt := range credits {
		if amt == 0 {
			continue
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO earned(addr,earned_atomic) VALUES($1,$2)
			 ON CONFLICT(addr) DO UPDATE SET earned_atomic = earned.earned_atomic + EXCLUDED.earned_atomic`,
			addr, int64(amt)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// dbGetEarned returns all miners' cumulative earned.
func dbGetEarned() (map[string]uint64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := pg.Query(ctx, `SELECT addr, earned_atomic FROM earned`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]uint64{}
	for rows.Next() {
		var a string
		var v int64
		if err := rows.Scan(&a, &v); err != nil {
			return nil, err
		}
		m[a] = uint64(v)
	}
	return m, rows.Err()
}

// ----- PPLNS sliding window (snapshot, NOT row-per-share) -----
//
// The PPLNS window is HOT: one append per accepted share (~100/s under load). Writing it to
// Postgres row-per-share is the classic mining-pool anti-pattern (MPOS's `shares` table) that
// collapses throughput under real concurrent load — every INSERT takes an index lock + WAL fsync,
// and a busy/replicated DB then back-pressures the submit handler. Following the canonical
// high-load design (open-ethereum-pool keeps the round in Redis; MiningCore batches inserts), the
// window lives IN MEMORY (st.PPLNS, same as pool.json mode) and is snapshotted WHOLE to a SINGLE
// row here every ~20s + on each block. The snapshot rides Patroni replication to the standby, so
// a promoted node resumes on the same window; at most one interval of weighting is lost on a hard
// failover (the money is `earned`, credited per block, which stays durable).

// dbSavePPLNSSnapshot upserts the whole window (compact JSON) into the single pplns_snapshot row.
func dbSavePPLNSSnapshot(windowJSON string, sum float64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO pplns_snapshot(id, window_json, sum_weight, updated_at)
		 VALUES(1,$1::jsonb,$2,now())
		 ON CONFLICT(id) DO UPDATE SET window_json=EXCLUDED.window_json,
		   sum_weight=EXCLUDED.sum_weight, updated_at=now()`,
		windowJSON, sum)
	return err
}

// dbLoadPPLNSSnapshot returns the latest window snapshot JSON (empty string if none yet).
func dbLoadPPLNSSnapshot() (string, float64, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var raw string
	var sum float64
	err := pg.QueryRow(ctx, `SELECT window_json::text, sum_weight FROM pplns_snapshot WHERE id=1`).Scan(&raw, &sum)
	if err == pgx.ErrNoRows {
		return "", 0, nil
	}
	return raw, sum, err
}

// ----- extranonce map snapshot (one row; replicated → a promoted standby keeps each miner's tag) -----

func dbSaveExtranonce(j string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO extranonce_snapshot(id, data, updated_at) VALUES(1,$1::jsonb,now())
		 ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`, j)
	return err
}

func dbLoadExtranonce() (string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var raw string
	err := pg.QueryRow(ctx, `SELECT data::text FROM extranonce_snapshot WHERE id=1`).Scan(&raw)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return raw, err
}

// ----- stats window snapshot (one row; lets a promoted standby keep live active/hashrate) -----

func dbSaveShareEv(j string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO shareev_snapshot(id, data, updated_at) VALUES(1,$1::jsonb,now())
		 ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data, updated_at=now()`, j)
	return err
}

func dbLoadShareEv() (string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var raw string
	err := pg.QueryRow(ctx, `SELECT data::text FROM shareev_snapshot WHERE id=1`).Scan(&raw)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return raw, err
}

// ----- inflight payouts + audit -----

func dbInflightAdd(txid, addr string, gross uint64, height uint64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO inflight(txid,addr,gross_atomic,sent_height) VALUES($1,$2,$3,$4)
		 ON CONFLICT(txid) DO NOTHING`,
		txid, addr, int64(gross), int64(height))
	return err
}

func dbInflightDelete(txid string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx, `DELETE FROM inflight WHERE txid=$1`, txid)
	return err
}

type inflightRow struct {
	Txid, Addr string
	Gross      uint64
	SentHeight uint64
}

func dbInflightList() ([]inflightRow, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := pg.Query(ctx, `SELECT txid,addr,gross_atomic,sent_height FROM inflight`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inflightRow
	for rows.Next() {
		var r inflightRow
		var g, h int64
		if err := rows.Scan(&r.Txid, &r.Addr, &g, &h); err != nil {
			return nil, err
		}
		r.Gross, r.SentHeight = uint64(g), uint64(h)
		out = append(out, r)
	}
	return out, rows.Err()
}

func dbPayoutLog(txid, addr string, amount, height uint64) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO payouts(txid,addr,amount_atomic,height) VALUES($1,$2,$3,$4)`,
		txid, addr, int64(amount), int64(height))
	return err
}

// ----- leader election (single-writer payout lease) -----

// dbLeaderAcquire atomically takes or renews the row-1 lease for `holder`, valid `ttl`.
// Returns true only if THIS holder now owns it (free / expired / already ours).
func dbLeaderAcquire(holder string, ttl time.Duration) (bool, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var got bool
	// make_interval(secs => $2) is unambiguous: $2 is typed as integer, so there is no
	// `int || text` operator-resolution gamble (which under pgx's extended protocol could
	// make the param infer as text and error out — silently killing the payout lease).
	err := pg.QueryRow(ctx,
		`UPDATE leader SET holder=$1, expires_at=now() + make_interval(secs => $2)
		 WHERE id=1 AND (holder=$1 OR holder IS NULL OR expires_at < now())
		 RETURNING true`,
		holder, int(ttl.Seconds())).Scan(&got)
	if err == pgx.ErrNoRows {
		return false, nil // someone else holds a live lease
	}
	if err != nil {
		return false, err
	}
	return got, nil
}

// ----- meta key/value -----

func dbMetaSet(k, v string) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := pg.Exec(ctx,
		`INSERT INTO meta(k,v) VALUES($1,$2) ON CONFLICT(k) DO UPDATE SET v=EXCLUDED.v`, k, v)
	return err
}

func dbMetaGet(k string) (string, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	var v string
	err := pg.QueryRow(ctx, `SELECT v FROM meta WHERE k=$1`, k).Scan(&v)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return v, err
}
