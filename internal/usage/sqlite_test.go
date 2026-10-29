package usage

import (
	"context"
	"database/sql"
	"testing"
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
