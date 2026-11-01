package usage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteRepo struct{ db *sql.DB }

// monthBounds returns the half-open [start, end) timestamps of a YYYY-MM month.
// Range comparisons keep idx_usage_created_at usable, unlike substr() filters.
func monthBounds(month string) (string, string, error) {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return "", "", err
	}
	return start.Format("2006-01-02 15:04:05"), start.AddDate(0, 1, 0).Format("2006-01-02 15:04:05"), nil
}

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
	// Legacy databases predate saved_cost; add the column if missing. A
	// duplicate-column error is the expected outcome on fresh/current tables;
	// any other failure must not be silently swallowed.
	if _, err := db.Exec(`ALTER TABLE usage_records ADD COLUMN saved_cost REAL`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil, err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_created_at ON usage_records(created_at);
		CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);`); err != nil {
		db.Close()
		return nil, err
	}
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
	start, end, err := monthBounds(yyyymm)
	if err != nil {
		return 0, err
	}
	var total sql.NullFloat64
	err = r.db.QueryRowContext(ctx,
		`SELECT SUM(cost) FROM usage_records WHERE created_at >= ? AND created_at < ?`, start, end).Scan(&total)
	return total.Float64, err
}

func (r *sqliteRepo) MonthlySaved(ctx context.Context, yyyymm string) (float64, error) {
	start, end, err := monthBounds(yyyymm)
	if err != nil {
		return 0, err
	}
	var total sql.NullFloat64
	err = r.db.QueryRowContext(ctx,
		`SELECT SUM(saved_cost) FROM usage_records WHERE created_at >= ? AND created_at < ?`, start, end).Scan(&total)
	return total.Float64, err
}

const aggColumns = `COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
	COALESCE(SUM(cost),0), COALESCE(SUM(saved_cost),0), COALESCE(SUM(cache_hit),0)`

const recordColumns = `id, model, provider, prompt_tokens, completion_tokens, cost, saved_cost, cache_hit, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanTotals(s scanner) (Totals, error) {
	var t Totals
	if err := s.Scan(&t.Requests, &t.PromptTokens, &t.CompletionTokens, &t.Cost, &t.Saved, &t.CacheHits); err != nil {
		return t, err
	}
	t.CacheHitRatio = ratio(t.CacheHits, t.Requests)
	return t, nil
}

func scanAgg(s scanner) (string, Totals, error) {
	var name string
	var t Totals
	if err := s.Scan(&name, &t.Requests, &t.PromptTokens, &t.CompletionTokens, &t.Cost, &t.Saved, &t.CacheHits); err != nil {
		return "", t, err
	}
	t.CacheHitRatio = ratio(t.CacheHits, t.Requests)
	return name, t, nil
}

func scanRecord(s scanner, rec *Record) error {
	var hit int
	var ts string
	if err := s.Scan(&rec.ID, &rec.Model, &rec.Provider, &rec.PromptTokens,
		&rec.CompletionTokens, &rec.Cost, &rec.SavedCost, &hit, &ts); err != nil {
		return err
	}
	rec.CacheHit = hit == 1
	rec.CreatedAt, _ = time.ParseInLocation("2006-01-02 15:04:05", ts, time.Local)
	return nil
}

func (r *sqliteRepo) List(ctx context.Context, limit int) ([]Record, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+recordColumns+` FROM usage_records ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		if err := scanRecord(rows, &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (r *sqliteRepo) Summary(ctx context.Context, month string) (Summary, error) {
	out := Summary{
		Month:      month,
		ByModel:    []ModelAgg{},
		ByProvider: []ProviderAgg{},
		ByDay:      []DayBucket{},
		ByHour:     []HourBucket{},
	}

	start, end, err := monthBounds(month)
	if err != nil {
		return out, err
	}

	totals, err := scanTotals(r.db.QueryRowContext(ctx,
		`SELECT `+aggColumns+` FROM usage_records WHERE created_at >= ? AND created_at < ?`, start, end))
	if err != nil {
		return out, err
	}
	out.Totals = totals

	rows, err := r.db.QueryContext(ctx,
		`SELECT model, `+aggColumns+` FROM usage_records
		 WHERE created_at >= ? AND created_at < ?
		 GROUP BY model ORDER BY SUM(cost) DESC`, start, end)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		name, t, err := scanAgg(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.ByModel = append(out.ByModel, ModelAgg{Model: name, Totals: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = r.db.QueryContext(ctx,
		`SELECT provider, `+aggColumns+` FROM usage_records
		 WHERE created_at >= ? AND created_at < ?
		 GROUP BY provider ORDER BY SUM(cost) DESC`, start, end)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		name, t, err := scanAgg(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.ByProvider = append(out.ByProvider, ProviderAgg{Provider: name, Totals: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = r.db.QueryContext(ctx,
		`SELECT substr(created_at,1,10) AS day, `+aggColumns+` FROM usage_records
		 WHERE created_at >= ? AND created_at < ?
		 GROUP BY day ORDER BY day`, start, end)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		name, t, err := scanAgg(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.ByDay = append(out.ByDay, DayBucket{Day: name, Totals: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	since := NowFunc().Add(-48 * time.Hour).Format("2006-01-02 15:04:05")
	rows, err = r.db.QueryContext(ctx,
		`SELECT substr(created_at,1,13) || ':00' AS hour, `+aggColumns+` FROM usage_records
		 WHERE created_at >= ? GROUP BY hour ORDER BY hour`, since)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		name, t, err := scanAgg(rows)
		if err != nil {
			return out, err
		}
		out.ByHour = append(out.ByHour, HourBucket{Hour: name, Totals: t})
	}
	return out, rows.Err()
}

func (r *sqliteRepo) Query(ctx context.Context, q Query) (int, []Record, error) {
	where := []string{"1=1"}
	args := []any{}
	if q.Model != "" {
		where = append(where, "model = ?")
		args = append(args, q.Model)
	}
	if q.Provider != "" {
		where = append(where, "provider = ?")
		args = append(args, q.Provider)
	}
	if q.Cache != nil {
		where = append(where, "cache_hit = ?")
		args = append(args, boolToInt(*q.Cache))
	}
	if q.Q != "" {
		where = append(where, "(id LIKE ? OR model LIKE ?)")
		like := "%" + q.Q + "%"
		args = append(args, like, like)
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM usage_records WHERE "+cond, args...).Scan(&total); err != nil {
		return 0, nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	pageArgs := append(append([]any{}, args...), limit, q.Offset)
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+recordColumns+" FROM usage_records WHERE "+cond+
			" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	out := []Record{}
	for rows.Next() {
		var rec Record
		if err := scanRecord(rows, &rec); err != nil {
			return 0, nil, err
		}
		out = append(out, rec)
	}
	return total, out, rows.Err()
}

func (r *sqliteRepo) Close() error { return r.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
