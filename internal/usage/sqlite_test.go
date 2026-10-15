package usage

import (
	"context"
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