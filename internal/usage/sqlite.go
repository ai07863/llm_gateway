package usage

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteRepo struct{ db *sql.DB }

func NewSQLite(path string) (Repo, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection keeps ":memory:" databases (and their schema)
	// visible to every query, since each connection would otherwise get
	// its own private in-memory database.
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS usage_records (
		id TEXT PRIMARY KEY,
		model TEXT, provider TEXT,
		prompt_tokens INTEGER, completion_tokens INTEGER,
		cost REAL, saved_cost REAL, cache_hit INTEGER, created_at TEXT)`)
	if err != nil {
		return nil, err
	}
	// Legacy databases predate saved_cost; add the column if missing.
	_, _ = db.Exec(`ALTER TABLE usage_records ADD COLUMN saved_cost REAL`)
	return &sqliteRepo{db: db}, nil
}

func (r *sqliteRepo) Record(ctx context.Context, rec Record) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = NowFunc()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO usage_records (id,model,provider,prompt_tokens,completion_tokens,cost,saved_cost,cache_hit,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Model, rec.Provider, rec.PromptTokens, rec.CompletionTokens, rec.Cost, rec.SavedCost, boolToInt(rec.CacheHit), rec.CreatedAt.Format("2006-01-02 15:04:05"))
	return err
}

func (r *sqliteRepo) MonthlyCost(ctx context.Context, yyyymm string) (float64, error) {
	var total sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		`SELECT SUM(cost) FROM usage_records WHERE substr(created_at,1,7) = ?`, yyyymm).Scan(&total)
	return total.Float64, err
}

func (r *sqliteRepo) MonthlySaved(ctx context.Context, yyyymm string) (float64, error) {
	var total sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		`SELECT SUM(saved_cost) FROM usage_records WHERE substr(created_at,1,7) = ?`, yyyymm).Scan(&total)
	return total.Float64, err
}

func (r *sqliteRepo) List(ctx context.Context, limit int) ([]Record, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id,model,provider,prompt_tokens,completion_tokens,cost,saved_cost,cache_hit,created_at
		 FROM usage_records ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		var hit int
		var ts string
		if err := rows.Scan(&rec.ID, &rec.Model, &rec.Provider, &rec.PromptTokens, &rec.CompletionTokens, &rec.Cost, &rec.SavedCost, &hit, &ts); err != nil {
			return nil, err
		}
		rec.CacheHit = hit == 1
		rec.CreatedAt, _ = time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *sqliteRepo) Close() error { return r.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
