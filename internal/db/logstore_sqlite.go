// logstore_sqlite.go — modernc.org/sqlite implementation of LogStore.
//
// Selected when LOG_BACKEND=sqlite (the default). Pure Go, no CGO, works
// in the alpine runtime image without pulling gcc.
//
//   File location: LOG_SQLITE_PATH (default /var/lib/foxrouters/logs.db)
//   Retention:     rows older than 90 days pruned by a background goroutine
//                  once per hour (mirrors the CH TTL semantics)
//   Concurrency:   sqlite serialises writes; we run WAL mode + a 5s busy
//                  timeout so reads and writes don't deadlock under load
//
// modernc.org/sqlite registers itself as the "sqlite" driver in database/sql.
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

const defaultSQLitePath = "/var/lib/foxrouters/logs.db"

// sqliteStore implements LogStore against a local file-backed SQLite DB.
type sqliteStore struct {
	db     *sql.DB
	path   string
	done   chan struct{}
	closed bool
}

func newSqliteStore() (LogStore, error) {
	path := envOr("LOG_SQLITE_PATH", defaultSQLitePath)
	// Create parent dir if writable — best effort; on read-only images the
	// user is expected to bind-mount the target.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			// Non-fatal: user might have pre-created the dir with tighter perms.
			slog.Warn("sqlite mkdir parent failed (continuing)", "module", "db-sqlite", "dir", dir, "error", err)
		}
	}

	// modernc.org/sqlite DSN mirrors mattn's format. Key pragmas:
	//   _journal_mode=WAL   → readers don't block writers
	//   _busy_timeout=5000  → 5s block on locked DB before returning SQLITE_BUSY
	//   _synchronous=NORMAL → fsync at commit only, fast + durable-enough for logs
	//   _foreign_keys=on    → future-proof
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(on)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %s: %w", path, err)
	}
	// SQLite writes serialise on a single connection — cap the pool.
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	// Verify the DB is actually reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("sqlite ping %s: %w", path, err)
	}

	// Migration: add cost_usd column if missing (idempotent).
	if _, err := sdb.ExecContext(ctx, `ALTER TABLE request_logs ADD COLUMN cost_usd REAL NOT NULL DEFAULT 0`); err != nil {
		// Column already exists (or table not yet created) — non-fatal.
		slog.Debug("sqlite migration cost_usd", "module", "db-sqlite", "note", "skip (already present or table pending)")
	}
	// Migration: add neurons column if missing (Cloudflare billing unit).
	if _, err := sdb.ExecContext(ctx, `ALTER TABLE request_logs ADD COLUMN neurons REAL NOT NULL DEFAULT 0`); err != nil {
		slog.Debug("sqlite migration neurons", "module", "db-sqlite", "note", "skip (already present or table pending)")
	}

	s := &sqliteStore{
		db:   sdb,
		path: path,
		done: make(chan struct{}),
	}
	slog.Info("sqlite log store ready", "module", "db-sqlite", "path", path)
	return s, nil
}

func (s *sqliteStore) Kind() string { return "sqlite" }

func (s *sqliteStore) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS request_logs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp     DATETIME NOT NULL,
			request_id    TEXT NOT NULL DEFAULT '',
			client_key    TEXT NOT NULL DEFAULT '',
			model         TEXT NOT NULL DEFAULT '',
			upstream      TEXT NOT NULL DEFAULT '',
			account_id    TEXT NOT NULL DEFAULT '',
			status_code   INTEGER NOT NULL DEFAULT 0,
			latency_ms    INTEGER NOT NULL DEFAULT 0,
			tokens_in INTEGER NOT NULL DEFAULT 0,
			tokens_out INTEGER NOT NULL DEFAULT 0,
			neurons REAL NOT NULL DEFAULT 0,
			cost_usd REAL NOT NULL DEFAULT 0,
			error_msg     TEXT NOT NULL DEFAULT '',
			input_text    TEXT NOT NULL DEFAULT '',
			output_text   TEXT NOT NULL DEFAULT '',
			request_body  TEXT NOT NULL DEFAULT '',
			response_body TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_timestamp ON request_logs(timestamp DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_model     ON request_logs(model)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_client    ON request_logs(client_key)`,

		`CREATE TABLE IF NOT EXISTS token_refresh_history (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp     DATETIME NOT NULL,
			account_email TEXT NOT NULL DEFAULT '',
			provider      TEXT NOT NULL DEFAULT '',
			success       INTEGER NOT NULL DEFAULT 0,
			error_msg     TEXT NOT NULL DEFAULT '',
			latency_ms    INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_refresh_timestamp ON token_refresh_history(timestamp DESC)`,

		`CREATE TABLE IF NOT EXISTS account_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp  DATETIME NOT NULL,
			account_id TEXT NOT NULL DEFAULT '',
			provider   TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL DEFAULT '',
			event_data TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_timestamp ON account_events(timestamp DESC)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite ensure schema: %w", err)
		}
	}
	// Start the TTL sweeper only after schema is confirmed.
	go s.retentionLoop()
	return nil
}

// retentionLoop periodically deletes rows older than 90 days across all
// three log tables — mirrors the ClickHouse TTL 90 DAY behaviour.
func (s *sqliteStore) retentionLoop() {
	// Run once shortly after startup, then hourly.
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	tick := time.NewTicker(1 * time.Hour)
	defer tick.Stop()

	prune := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, tbl := range []string{"request_logs", "token_refresh_history", "account_events"} {
			q := fmt.Sprintf(`DELETE FROM %s WHERE timestamp < datetime('now', '-90 days')`, tbl)
			if _, err := s.db.ExecContext(ctx, q); err != nil {
				slog.Warn("sqlite retention prune failed", "module", "db-sqlite", "table", tbl, "error", err)
			}
		}
	}

	for {
		select {
		case <-first.C:
			prune()
		case <-tick.C:
			prune()
		case <-s.done:
			return
		}
	}
}

func (s *sqliteStore) InsertRequestBatch(ctx context.Context, batch []RequestLog) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		// Rollback is a no-op if Commit already succeeded.
		_ = tx.Rollback()
	}()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO request_logs (
		timestamp, request_id, client_key, model, upstream, account_id,
		status_code, latency_ms, tokens_in, tokens_out, neurons, cost_usd, error_msg,
		input_text, output_text, request_body, response_body
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range batch {
		ts := r.Timestamp.UTC()
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if _, err := stmt.ExecContext(ctx,
			ts,
			r.RequestID,
			r.ClientKey,
			r.Model,
			r.Upstream,
			r.AccountID,
			r.StatusCode,
			max0(r.LatencyMs),
			max0(r.TokensIn),
			max0(r.TokensOut),
			r.Neurons,
			r.CostUsd,
			r.ErrorMsg,
			r.InputText,
			r.OutputText,
			bodyString(r.RequestBody),
			bodyString(r.ResponseBody),
		); err != nil {
			slog.Error("sqlite reqlog insert", "module", "db-sqlite", "error", err)
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) InsertRefreshBatch(ctx context.Context, batch []RefreshLog) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO token_refresh_history (
		timestamp, account_email, provider, success, error_msg, latency_ms
	) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range batch {
		suc := 0
		if r.Success {
			suc = 1
		}
		ts := r.Timestamp.UTC()
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if _, err := stmt.ExecContext(ctx, ts, r.AccountEmail, r.Provider, suc, r.ErrorMsg, max0(r.LatencyMs)); err != nil {
			slog.Error("sqlite refresh insert", "module", "db-sqlite", "error", err)
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) InsertEventBatch(ctx context.Context, batch []AccountEvent) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO account_events (
		timestamp, account_id, provider, event_type, event_data
	) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range batch {
		var data string
		if e.EventData != nil {
			raw, _ := json.Marshal(e.EventData)
			data = string(raw)
		}
		ts := e.Timestamp.UTC()
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		if _, err := stmt.ExecContext(ctx, ts, e.AccountID, e.Provider, e.EventType, data); err != nil {
			slog.Error("sqlite event insert", "module", "db-sqlite", "error", err)
		}
	}
	return tx.Commit()
}

func (s *sqliteStore) GetRequestStats(ctx context.Context, since time.Time) (*RequestStats, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(tokens_in), 0),
			COALESCE(SUM(tokens_out), 0),
			COALESCE(SUM(cost_usd), 0)
		FROM request_logs
		WHERE timestamp >= ?
	`, since.UTC())

	stats := &RequestStats{}
	var total, errors, tin, tout int64
	var avg, cost float64
	if err := row.Scan(&total, &errors, &avg, &tin, &tout, &cost); err != nil {
		return nil, err
	}
	stats.TotalRequests = int(total)
	stats.TotalErrors = int(errors)
	stats.AvgLatencyMs = avg
	stats.TotalTokensIn = int(tin)
	stats.TotalTokensOut = int(tout)
	stats.TotalTokens = int(tin + tout)
	stats.TotalCostUsd = cost
	if stats.TotalRequests > 0 {
		stats.ErrorRate = float64(stats.TotalErrors) / float64(stats.TotalRequests) * 100
	}
	return stats, nil
}

func (s *sqliteStore) GetModelStats(ctx context.Context, since time.Time, limit int) ([]ModelStats, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			model,
			COUNT(*) AS total_requests,
			SUM(CASE WHEN status_code >= 400 THEN 1 ELSE 0 END) AS total_errors,
			COALESCE(AVG(latency_ms), 0) AS avg_latency,
			COALESCE(SUM(tokens_in), 0) AS tokens_in,
			COALESCE(SUM(tokens_out), 0) AS tokens_out,
			COALESCE(SUM(cost_usd), 0) AS cost_usd
		FROM request_logs
		WHERE timestamp >= ?
		GROUP BY model
		ORDER BY total_requests DESC
		LIMIT ?
	`, since.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelStats
	for rows.Next() {
		var m ModelStats
		var tr, te, tin, tout int64
		var avg, cost float64
		if err := rows.Scan(&m.Model, &tr, &te, &avg, &tin, &tout, &cost); err != nil {
			continue
		}
		m.TotalRequests = int(tr)
		m.TotalErrors = int(te)
		m.AvgLatencyMs = avg
		m.TotalTokensIn = int(tin)
		m.TotalTokensOut = int(tout)
		m.TotalTokens = int(tin + tout)
		m.TotalCostUsd = cost
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite model stats iteration: %w", err)
	}
	return out, nil
}

func (s *sqliteStore) GetRecentRequests(ctx context.Context, limit int) ([]RecentRequest, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, timestamp, client_key, model, upstream, account_id,
		       status_code, latency_ms, tokens_in, tokens_out, error_msg,
		       input_text, output_text
		FROM request_logs
		ORDER BY timestamp DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RecentRequest, 0, limit)
	for rows.Next() {
		var r RecentRequest
		var id int64
		var ts time.Time
		if err := rows.Scan(&id, &ts, &r.ClientKey, &r.Model, &r.Upstream,
			&r.AccountID, &r.StatusCode, &r.LatencyMs, &r.TokensIn, &r.TokensOut,
			&r.ErrorMsg, &r.InputText, &r.OutputText); err != nil {
			slog.Error("sqlite recent scan", "module", "db-sqlite", "error", err)
			continue
		}
		// ID is stringified for JS precision parity with the CH backend
		// (the frontend sends this exact value back on /history/detail/:id).
		r.ID = strconv.FormatInt(id, 10)
		r.Timestamp = ts.UTC().Format("2006-01-02 15:04:05")
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		slog.Error("sqlite recent iteration", "module", "db-sqlite", "error", err)
	}
	return out, nil
}

func (s *sqliteStore) GetRequestDetail(ctx context.Context, id uint64) (*RequestDetail, error) {
	// Callers pass a uint64 — SQLite's PK is signed int64 but we only ever
	// insert positive AUTOINCREMENT values, so the range fits.
	var d RequestDetail
	var rawID int64
	var ts time.Time
	var reqBody, respBody string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, timestamp, client_key, model, upstream, account_id,
		       status_code, latency_ms, tokens_in, tokens_out, error_msg,
		       input_text, output_text, request_body, response_body
		FROM request_logs
		WHERE id = ?
		LIMIT 1
	`, int64(id)).Scan(&rawID, &ts, &d.ClientKey, &d.Model, &d.Upstream,
		&d.AccountID, &d.StatusCode, &d.LatencyMs, &d.TokensIn, &d.TokensOut,
		&d.ErrorMsg, &d.InputText, &d.OutputText, &reqBody, &respBody)
	if err != nil {
		return nil, err
	}
	d.ID = strconv.FormatInt(rawID, 10)
	d.Timestamp = ts.UTC().Format("2006-01-02 15:04:05")
	if reqBody != "" {
		d.RequestBody = []byte(reqBody)
	}
	if respBody != "" {
		d.ResponseBody = []byte(respBody)
	}
	return &d, nil
}

func (s *sqliteStore) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
