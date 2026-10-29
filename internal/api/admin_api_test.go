package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

func newUsageRepo(t *testing.T, name string) usage.Repo {
	t.Helper()
	repo, err := usage.NewSQLite("file:" + name + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo
}

func TestSummaryEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newUsageRepo(t, "summary")
	ctx := context.Background()
	if err := repo.Record(ctx, usage.Record{ID: "s1", Model: "m1", Provider: "p1", PromptTokens: 100, CompletionTokens: 50, Cost: 0.25}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := repo.Record(ctx, usage.Record{ID: "s2", Model: "m2", Provider: "p1", SavedCost: 0.5, CacheHit: true}); err != nil {
		t.Fatalf("record: %v", err)
	}

	r := gin.New()
	registerSummary(r.Group("/v1"), repo, config.Settings{BudgetMonthlyLimit: 10, BudgetSoftRatio: 0.8})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var body struct {
		Month  string `json:"month"`
		Totals struct {
			Requests int     `json:"requests"`
			Cost     float64 `json:"cost"`
			Saved    float64 `json:"saved"`
			CacheHit float64 `json:"cache_hit_ratio"`
		} `json:"totals"`
		ByModel []struct {
			Model string `json:"model"`
		} `json:"by_model"`
		ByDay []struct {
			Day string `json:"day"`
		} `json:"by_day"`
		Budget struct {
			Limit     float64 `json:"limit"`
			UsedRatio float64 `json:"used_ratio"`
			Degraded  bool    `json:"degraded"`
			Rejected  bool    `json:"rejected"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Month != usage.CurrentMonth() {
		t.Fatalf("month: %q", body.Month)
	}
	if body.Totals.Requests != 2 || body.Totals.Cost < 0.24 || body.Totals.Saved < 0.49 || body.Totals.CacheHit != 0.5 {
		t.Fatalf("totals: %+v", body.Totals)
	}
	if len(body.ByModel) != 2 || len(body.ByDay) != 1 {
		t.Fatalf("buckets: models=%d days=%d", len(body.ByModel), len(body.ByDay))
	}
	if body.Budget.Limit != 10 || body.Budget.UsedRatio < 0.024 || body.Budget.UsedRatio > 0.026 {
		t.Fatalf("budget: %+v", body.Budget)
	}
	if body.Budget.Degraded || body.Budget.Rejected {
		t.Fatalf("budget flags: %+v", body.Budget)
	}
}

func TestSummaryRejectsBadMonth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSummary(r.Group("/v1"), newUsageRepo(t, "summarybad"), config.Settings{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/summary?month=2026-13", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestSummaryWithoutBudgetReportsZeroLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerSummary(r.Group("/v1"), newUsageRepo(t, "summarynobudget"), config.Settings{BudgetMonthlyLimit: 0})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/summary", nil))
	var body struct {
		Budget struct {
			Limit     float64 `json:"limit"`
			UsedRatio float64 `json:"used_ratio"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Budget.Limit != 0 || body.Budget.UsedRatio != 0 {
		t.Fatalf("disabled budget: %+v", body.Budget)
	}
}