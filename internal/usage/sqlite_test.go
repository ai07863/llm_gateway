package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRecordAndMonthly(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	if err := repo.Record(ctx, Record{ID: "1", Model: "deepseek-chat", Provider: "deepseek", PromptTokens: 10, CompletionTokens: 5, Cost: 0.02}); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, err := repo.MonthlyCost(ctx, CurrentMonth())
	if err != nil {
		t.Fatalf("monthly: %v", err)
	}
	if got < 0.019 || got > 0.021 {
		t.Fatalf("want ~0.02, got %v", got)
	}
}

func TestSQLiteMonthlySaved(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	if err := repo.Record(ctx, Record{ID: "h", Model: "deepseek-chat", Provider: "cache", Cost: 0, SavedCost: 0.5, CacheHit: true}); err != nil {
		t.Fatalf("record: %v", err)
	}
	saved, err := repo.MonthlySaved(ctx, CurrentMonth())
	if err != nil {
		t.Fatalf("monthly saved: %v", err)
	}
	if saved < 0.49 || saved > 0.51 {
		t.Fatalf("want ~0.5, got %v", saved)
	}
	if cost, _ := repo.MonthlyCost(ctx, CurrentMonth()); cost != 0 {
		t.Fatalf("cache hits must not add to cost, got %v", cost)
	}
}

func TestSQLiteMigratesLegacyTableWithoutSavedCost(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE usage_records (
		id TEXT PRIMARY KEY, model TEXT, provider TEXT,
		prompt_tokens INTEGER, completion_tokens INTEGER,
		cost REAL, cache_hit INTEGER, created_at TEXT)`); err != nil {
		t.Fatalf("create legacy: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	repo, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("NewSQLite on legacy db: %v", err)
	}
	defer repo.Close()

	if err := repo.Record(context.Background(), Record{ID: "x", Model: "m", SavedCost: 0.25}); err != nil {
		t.Fatalf("record into migrated table: %v", err)
	}
	saved, err := repo.MonthlySaved(context.Background(), CurrentMonth())
	if err != nil {
		t.Fatalf("monthly saved: %v", err)
	}
	if saved < 0.24 || saved > 0.26 {
		t.Fatalf("want ~0.25, got %v", saved)
	}
}

func withNow(t *testing.T, at time.Time) {
	t.Helper()
	old := NowFunc
	NowFunc = func() time.Time { return at }
	t.Cleanup(func() { NowFunc = old })
}

func insertAt(t *testing.T, repo Repo, id, model, provider string, cost, saved float64, hit bool, at time.Time) {
	t.Helper()
	err := repo.Record(context.Background(), Record{
		ID: id, Model: model, Provider: provider,
		PromptTokens: 10, CompletionTokens: 5,
		Cost: cost, SavedCost: saved, CacheHit: hit, CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func TestSQLiteSummaryAggregatesCurrentMonth(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	withNow(t, now)
	insertAt(t, repo, "a", "m1", "p1", 0.2, 0, false, now)
	insertAt(t, repo, "b", "m1", "p1", 0.1, 0, true, now.Add(-time.Hour))
	insertAt(t, repo, "c", "m2", "p2", 9.9, 0, false, now.AddDate(0, -1, 0))

	sum, err := repo.Summary(context.Background(), CurrentMonth())
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Totals.Requests != 2 {
		t.Fatalf("requests: want 2, got %d", sum.Totals.Requests)
	}
	if sum.Totals.PromptTokens != 20 || sum.Totals.CompletionTokens != 10 {
		t.Fatalf("tokens: %+v", sum.Totals)
	}
	if sum.Totals.Cost < 0.29 || sum.Totals.Cost > 0.31 {
		t.Fatalf("cost: %v", sum.Totals.Cost)
	}
	if sum.Totals.CacheHits != 1 || sum.Totals.CacheHitRatio != 0.5 {
		t.Fatalf("cache: %+v", sum.Totals)
	}
	if len(sum.ByModel) != 1 || sum.ByModel[0].Model != "m1" || sum.ByModel[0].Requests != 2 {
		t.Fatalf("by_model: %+v", sum.ByModel)
	}
	if len(sum.ByProvider) != 1 || sum.ByProvider[0].Provider != "p1" {
		t.Fatalf("by_provider: %+v", sum.ByProvider)
	}
	if len(sum.ByDay) != 1 || sum.ByDay[0].Requests != 2 {
		t.Fatalf("by_day: %+v", sum.ByDay)
	}
	var hourRequests int
	for _, h := range sum.ByHour {
		hourRequests += h.Requests
	}
	if hourRequests != 2 {
		t.Fatalf("by_hour should cover the last 48h: %+v", sum.ByHour)
	}
}

func TestSQLiteQueryFiltersAndPaginates(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	withNow(t, now)
	insertAt(t, repo, "r1", "alpha", "p1", 0.1, 0, false, now.Add(-3*time.Minute))
	insertAt(t, repo, "r2", "beta", "p1", 0.2, 0.4, true, now.Add(-2*time.Minute))
	insertAt(t, repo, "r3", "alpha", "p2", 0.3, 0, false, now.Add(-time.Minute))
	insertAt(t, repo, "r4", "alpha", "p1", 0.4, 0.8, true, now)

	ctx := context.Background()
	hit := true
	miss := false

	total, records, err := repo.Query(ctx, Query{Model: "alpha", Limit: 10})
	if err != nil {
		t.Fatalf("query model: %v", err)
	}
	if total != 3 || len(records) != 3 {
		t.Fatalf("model filter: total=%d len=%d", total, len(records))
	}

	total, records, err = repo.Query(ctx, Query{Cache: &hit, Limit: 10})
	if err != nil {
		t.Fatalf("query hit: %v", err)
	}
	if total != 2 || records[0].ID != "r4" || records[1].ID != "r2" {
		t.Fatalf("cache hit + order: total=%d records=%+v", total, records)
	}

	total, records, err = repo.Query(ctx, Query{Cache: &miss, Provider: "p1", Limit: 10})
	if err != nil {
		t.Fatalf("query miss: %v", err)
	}
	if total != 1 || records[0].ID != "r1" {
		t.Fatalf("miss + provider: total=%d records=%+v", total, records)
	}

	total, records, err = repo.Query(ctx, Query{Q: "r2", Limit: 10})
	if err != nil {
		t.Fatalf("query q: %v", err)
	}
	if total != 1 || records[0].ID != "r2" {
		t.Fatalf("search: total=%d records=%+v", total, records)
	}

	total, records, err = repo.Query(ctx, Query{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("query page: %v", err)
	}
	if total != 4 || len(records) != 2 || records[0].ID != "r3" {
		t.Fatalf("pagination: total=%d records=%+v", total, records)
	}
}

func TestMonthBounds(t *testing.T) {
	start, end, err := monthBounds("2026-10")
	if err != nil {
		t.Fatalf("bounds: %v", err)
	}
	if start != "2026-10-01 00:00:00" || end != "2026-11-01 00:00:00" {
		t.Fatalf("unexpected bounds: %q .. %q", start, end)
	}
	if _, _, err := monthBounds("2026-13"); err == nil {
		t.Fatal("want error for invalid month")
	}
}

func TestSummaryHonoursMonthBoundaries(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	at := func(ts string) time.Time {
		tm, perr := time.Parse("2006-01-02 15:04:05", ts)
		if perr != nil {
			t.Fatalf("parse %q: %v", ts, perr)
		}
		return tm
	}
	for _, rec := range []Record{
		{ID: "before", Model: "m", Provider: "p", Cost: 1, CreatedAt: at("2026-09-30 23:59:59")},
		{ID: "start", Model: "m", Provider: "p", Cost: 2, CreatedAt: at("2026-10-01 00:00:00")},
		{ID: "end", Model: "m", Provider: "p", Cost: 4, CreatedAt: at("2026-10-31 23:59:59")},
		{ID: "after", Model: "m", Provider: "p", Cost: 8, CreatedAt: at("2026-11-01 00:00:00")},
	} {
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record %s: %v", rec.ID, err)
		}
	}

	sum, err := repo.Summary(ctx, "2026-10")
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.Totals.Cost != 6 || sum.Totals.Requests != 2 {
		t.Fatalf("month must include only October rows: %+v", sum.Totals)
	}
}

func TestSQLiteFileEnablesWALAndBusyTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	repo, err := NewSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	sr := repo.(*sqliteRepo)
	var mode string
	if err := sr.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Fatalf("want WAL journal mode, got %q", mode)
	}
	var timeout int
	if err := sr.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if timeout < 1000 {
		t.Fatalf("busy_timeout too small: %d", timeout)
	}
}

func TestDeleteBefore(t *testing.T) {
	repo, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()

	ctx := context.Background()
	old := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	for _, rec := range []Record{
		{ID: "old", Model: "m", Provider: "p", Cost: 1, CreatedAt: old},
		{ID: "fresh", Model: "m", Provider: "p", Cost: 1, CreatedAt: fresh},
	} {
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record %s: %v", rec.ID, err)
		}
	}

	n, err := repo.DeleteBefore(ctx, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 deleted row, got %d", n)
	}
	recs, err := repo.List(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "fresh" {
		t.Fatalf("unexpected remaining rows: %+v", recs)
	}
}
