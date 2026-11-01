package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/metrics"
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

func TestRequestsEndpointFiltersAndPaginates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newUsageRepo(t, "requests")
	ctx := context.Background()
	for _, rec := range []usage.Record{
		{ID: "q1", Model: "alpha", Provider: "p1", Cost: 0.1},
		{ID: "q2", Model: "beta", Provider: "p1", Cost: 0.2, SavedCost: 0.4, CacheHit: true},
		{ID: "q3", Model: "alpha", Provider: "p2", Cost: 0.3},
	} {
		if err := repo.Record(ctx, rec); err != nil {
			t.Fatalf("record %s: %v", rec.ID, err)
		}
	}

	r := gin.New()
	registerRequests(r.Group("/v1"), repo)
	get := func(query string) (int, int, []usage.Record) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/requests"+query, nil))
		var body struct {
			Total   int            `json:"total"`
			Records []usage.Record `json:"records"`
		}
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return w.Code, body.Total, body.Records
	}

	code, total, records := get("?model=alpha&limit=10")
	if code != http.StatusOK || total != 2 || len(records) != 2 || records[0].ID != "q3" {
		t.Fatalf("model filter: code=%d total=%d records=%+v", code, total, records)
	}

	code, total, records = get("?cache=hit")
	if code != http.StatusOK || total != 1 || records[0].ID != "q2" {
		t.Fatalf("cache hit: code=%d total=%d records=%+v", code, total, records)
	}

	code, total, records = get("?provider=p1&cache=miss")
	if code != http.StatusOK || total != 1 || records[0].ID != "q1" {
		t.Fatalf("provider+miss: code=%d total=%d records=%+v", code, total, records)
	}

	code, total, records = get("?q=q2")
	if code != http.StatusOK || total != 1 || records[0].ID != "q2" {
		t.Fatalf("search: code=%d total=%d records=%+v", code, total, records)
	}

	code, total, records = get("?limit=2&offset=1")
	if code != http.StatusOK || total != 3 || len(records) != 2 || records[0].ID != "q2" || records[1].ID != "q1" {
		t.Fatalf("pagination: code=%d total=%d records=%+v", code, total, records)
	}

	if code, _, _ := get("?cache=maybe"); code != http.StatusBadRequest {
		t.Fatalf("invalid cache filter: want 400, got %d", code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := metrics.New()
	m.Requests.WithLabelValues("p1", "200").Add(9)
	m.Requests.WithLabelValues("p1", "500").Inc()
	m.Latency.WithLabelValues("p1").Observe(0.2)

	r := gin.New()
	registerHealth(r.Group("/v1"), m)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var body struct {
		RequestsTotal int     `json:"requests_total"`
		ErrorRate     float64 `json:"error_rate"`
		Latency       struct {
			Avg float64 `json:"avg"`
			P50 float64 `json:"p50"`
			P95 float64 `json:"p95"`
		} `json:"latency_ms"`
		ByStatus []struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		} `json:"by_status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RequestsTotal != 10 || body.ErrorRate < 0.09 || body.ErrorRate > 0.11 {
		t.Fatalf("counts: %+v", body)
	}
	if body.Latency.Avg <= 0 || body.Latency.P50 <= 0 {
		t.Fatalf("latency: %+v", body.Latency)
	}
	if len(body.ByStatus) != 2 {
		t.Fatalf("by_status: %+v", body.ByStatus)
	}
}

type snapshotRepo struct {
	usage.Repo
	summary      usage.Summary
	monthlyCalls int
}

func (r *snapshotRepo) Summary(ctx context.Context, month string) (usage.Summary, error) {
	return r.summary, nil
}

func (r *snapshotRepo) MonthlyCost(ctx context.Context, month string) (float64, error) {
	r.monthlyCalls++
	return 999, nil
}

func (r *snapshotRepo) MonthlySaved(ctx context.Context, month string) (float64, error) {
	r.monthlyCalls++
	return 999, nil
}

func TestSummaryBudgetUsesSameSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &snapshotRepo{summary: usage.Summary{
		Month:  "2026-10",
		Totals: usage.Totals{Cost: 0.25, Saved: 0.5},
	}}

	r := gin.New()
	registerSummary(r.Group("/v1"), repo, config.Settings{BudgetMonthlyLimit: 10})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/summary?month=2026-10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var body struct {
		Budget struct {
			Cost  float64 `json:"cost"`
			Saved float64 `json:"saved"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Budget.Cost != 0.25 || body.Budget.Saved != 0.5 {
		t.Fatalf("budget must reuse summary totals: %+v", body.Budget)
	}
	if repo.monthlyCalls != 0 {
		t.Fatalf("summary endpoint must not issue extra monthly queries, got %d", repo.monthlyCalls)
	}
}

func TestMetricsEndpointRequiresToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:metricsauth?mode=memory&cache=shared",
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", w.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 with token, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "<html") {
		t.Fatal("metrics endpoint must not serve the dashboard")
	}
}

func TestAdminEndpointsRequireToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:adminwire1?mode=memory&cache=shared",
	})
	for _, path := range []string{"/v1/admin/summary", "/v1/admin/requests", "/v1/admin/health"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, w.Code)
		}
	}
}

func TestAdminEndpointsWorkWithToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := NewRouter(config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:adminwire2?mode=memory&cache=shared",
	})
	for _, path := range []string{"/v1/admin/summary", "/v1/admin/requests", "/v1/admin/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", path, w.Code, w.Body.String())
		}
	}
}
