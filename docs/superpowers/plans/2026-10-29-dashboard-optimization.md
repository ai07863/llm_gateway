# Dashboard Optimization Implementation Plan

> **For the implementer:** Use TDD. Write the failing test, run it, watch it fail, write the minimal code, run it, watch it pass, commit.

**Goal:** Upgrade the gateway dashboard to show cost analysis, budget status, runtime health, day/hour trends, and filterable paginated request details, with zero external network dependencies.

**Architecture:** Add read-only aggregation endpoints backed by SQLite (`/v1/admin/summary`, `/v1/admin/requests`, `/v1/admin/health`), expose an in-process metrics snapshot, vendor Chart.js as an embedded asset, and rewrite the single-page dashboard to consume those endpoints plus the existing SSE stream.

**Tech Stack:** Go, Gin, SQLite (modernc `sqlite`), Prometheus client_golang, vanilla JS, Chart.js 4.4.9 (vendored).

**Design spec:** `docs/superpowers/specs/2026-10-29-dashboard-optimization-design.md`

**Conventions:** Windows/PowerShell, no code comments unless necessary, admin routes are behind `AuthMiddleware`, dashboard route is public but serves only static assets.

---

## File Map

| File | Responsibility |
|---|---|
| `internal/usage/usage.go` | Add `Totals`, `ModelAgg`, `ProviderAgg`, `DayBucket`, `HourBucket`, `Summary`, `Query`; extend `Repo` |
| `internal/usage/sqlite.go` | Implement `Summary` + `Query`, add indexes, share scan helpers |
| `internal/usage/sqlite_test.go` | Aggregation and query tests |
| `internal/metrics/metrics.go` | Add `Snapshot()` + percentile helper |
| `internal/metrics/metrics_test.go` | Snapshot tests |
| `internal/api/summary.go` | `GET /v1/admin/summary` |
| `internal/api/requests.go` | `GET /v1/admin/requests` |
| `internal/api/health.go` | `GET /v1/admin/health` |
| `internal/api/admin_api_test.go` | Endpoint tests + router wiring test |
| `internal/api/router.go` | Register new routes, pass `repo` and `m` |
| `internal/api/dashboard.go` | Embed and serve `web/chart.umd.min.js` |
| `internal/api/dashboard_test.go` | Asset tests + updated page assertions |
| `internal/api/web/chart.umd.min.js` | Vendored Chart.js |
| `internal/api/web/index.html` | Rewritten dashboard |
| `bin/gateway.exe` | Rebuilt binary for manual verification |

---

## Task 1: Usage aggregation types and SQLite implementation

**Files:**
- Modify: `internal/usage/usage.go`
- Modify: `internal/usage/sqlite.go`
- Test: `internal/usage/sqlite_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/usage/sqlite_test.go` (add `time` to imports):

```go
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
```

- [ ] **Step 2: Run tests and confirm failure**

Run: `go test ./internal/usage/ -run "TestSQLiteSummary|TestSQLiteQuery" -v`

Expected: compile error (`Summary`/`Query` undefined).

- [ ] **Step 3: Add types to `internal/usage/usage.go`**

Append below `Record`:

```go
type Totals struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	Saved            float64 `json:"saved"`
	CacheHits        int     `json:"cache_hits"`
	CacheHitRatio    float64 `json:"cache_hit_ratio"`
}

type ModelAgg struct {
	Model string `json:"model"`
	Totals
}

type ProviderAgg struct {
	Provider string `json:"provider"`
	Totals
}

type DayBucket struct {
	Day string `json:"day"`
	Totals
}

type HourBucket struct {
	Hour string `json:"hour"`
	Totals
}

type Summary struct {
	Month      string        `json:"month"`
	Totals     Totals        `json:"totals"`
	ByModel    []ModelAgg    `json:"by_model"`
	ByProvider []ProviderAgg `json:"by_provider"`
	ByDay      []DayBucket   `json:"by_day"`
	ByHour     []HourBucket  `json:"by_hour"`
}

type Query struct {
	Model    string
	Provider string
	Cache    *bool
	Q        string
	Limit    int
	Offset   int
}

func ratio(hits, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}
```

Extend `Repo`:

```go
type Repo interface {
	Record(ctx context.Context, rec Record) error
	MonthlyCost(ctx context.Context, month string) (float64, error)
	MonthlySaved(ctx context.Context, month string) (float64, error)
	List(ctx context.Context, limit int) ([]Record, error)
	Summary(ctx context.Context, month string) (Summary, error)
	Query(ctx context.Context, q Query) (int, []Record, error)
	Close() error
}
```

- [ ] **Step 4: Implement helpers + `Summary` + `Query` in `internal/usage/sqlite.go`**

Add near the top of the file:

```go
const aggColumns = `COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
	COALESCE(SUM(cost),0), COALESCE(SUM(saved_cost),0), COALESCE(SUM(cache_hit),0)`

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
```

Add indexes inside `NewSQLite` after the existing migration block and before `return`:

```go
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_usage_created_at ON usage_records(created_at);
		CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_records(model);`); err != nil {
		db.Close()
		return nil, err
	}
```

Append methods:

```go
func (r *sqliteRepo) Summary(ctx context.Context, month string) (Summary, error) {
	out := Summary{
		Month:      month,
		ByModel:    []ModelAgg{},
		ByProvider: []ProviderAgg{},
		ByDay:      []DayBucket{},
		ByHour:     []HourBucket{},
	}

	totals, err := scanTotals(r.db.QueryRowContext(ctx,
		`SELECT `+aggColumns+` FROM usage_records WHERE substr(created_at,1,7) = ?`, month))
	if err != nil {
		return out, err
	}
	out.Totals = totals

	rows, err := r.db.QueryContext(ctx,
		`SELECT model, `+aggColumns+` FROM usage_records
		 WHERE substr(created_at,1,7) = ? GROUP BY model ORDER BY SUM(cost) DESC`, month)
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
		 WHERE substr(created_at,1,7) = ? GROUP BY provider ORDER BY SUM(cost) DESC`, month)
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
		 WHERE substr(created_at,1,7) = ? GROUP BY day ORDER BY day`, month)
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
```

Reuse the existing `Record` column list / scan code from `List`. If `List` scans inline, extract:

```go
const recordColumns = `id, model, provider, prompt_tokens, completion_tokens, cost, saved_cost, cache_hit, created_at`

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
```

`boolToInt` already exists in `sqlite.go`; do not redeclare it. Rewrite `List` to use `recordColumns` + `scanRecord` (behavior unchanged).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/usage/ -v`

Expected: PASS (all existing + new).

- [ ] **Step 6: Commit**

```powershell
git add internal/usage
git commit -m "feat(usage): add summary aggregation and filtered query"
```

---

## Task 2: Metrics snapshot

**Files:**
- Modify: `internal/metrics/metrics.go`
- Test: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/metrics/metrics_test.go` (add `math` to imports):

```go
func TestSnapshotCountsAndErrorRate(t *testing.T) {
	reg := New()
	reg.Requests.WithLabelValues("p1", "200").Add(8)
	reg.Requests.WithLabelValues("p1", "429").Add(2)

	s := reg.Snapshot()
	if s.RequestsTotal != 10 {
		t.Fatalf("requests total: %d", s.RequestsTotal)
	}
	if math.Abs(s.ErrorRate-0.2) > 1e-9 {
		t.Fatalf("error rate: %v", s.ErrorRate)
	}
	if len(s.ByStatus) != 2 {
		t.Fatalf("by_status: %+v", s.ByStatus)
	}
}

func TestSnapshotLatencyPercentiles(t *testing.T) {
	reg := New()
	for _, v := range []float64{0.1, 0.1, 0.2, 0.2, 5.0} {
		reg.Latency.WithLabelValues("p1").Observe(v)
	}

	s := reg.Snapshot()
	if s.LatencyMS.Avg < 1000 || s.LatencyMS.Avg > 1200 {
		t.Fatalf("avg: %v", s.LatencyMS.Avg)
	}
	if s.LatencyMS.P50 <= 0 || s.LatencyMS.P50 >= s.LatencyMS.P95 {
		t.Fatalf("percentiles: %+v", s.LatencyMS)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	s := New().Snapshot()
	if s.RequestsTotal != 0 || s.ErrorRate != 0 || s.LatencyMS.P95 != 0 {
		t.Fatalf("empty snapshot should be zero: %+v", s)
	}
	if s.ByStatus == nil {
		t.Fatal("by_status must be an empty slice, not null")
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/metrics/ -v`

Expected: `Snapshot` undefined.

- [ ] **Step 3: Implement**

Add to `internal/metrics/metrics.go` (add `math`, `sort`, `strings` imports as needed):

```go
type StatusCount struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Count    int    `json:"count"`
}

type LatencyMS struct {
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

type Snapshot struct {
	RequestsTotal int           `json:"requests_total"`
	ErrorRate     float64       `json:"error_rate"`
	LatencyMS     LatencyMS     `json:"latency_ms"`
	ByStatus      []StatusCount `json:"by_status"`
}

func (m *Registry) Snapshot() Snapshot {
	out := Snapshot{ByStatus: []StatusCount{}}
	families, err := m.reg.Gather()
	if err != nil {
		return out
	}

	var errCount int
	var latencyCount uint64
	var latencySum float64
	buckets := map[float64]uint64{}

	for _, family := range families {
		switch family.GetName() {
		case "gateway_requests_total":
			for _, metric := range family.GetMetric() {
				labels := map[string]string{}
				for _, pair := range metric.GetLabel() {
					labels[pair.GetName()] = pair.GetValue()
				}
				count := int(metric.GetCounter().GetValue())
				out.RequestsTotal += count
				if !strings.HasPrefix(labels["status"], "2") {
					errCount += count
				}
				out.ByStatus = append(out.ByStatus, StatusCount{
					Provider: labels["provider"],
					Status:   labels["status"],
					Count:    count,
				})
			}
		case "gateway_request_duration_seconds":
			for _, metric := range family.GetMetric() {
				h := metric.GetHistogram()
				latencyCount += h.GetSampleCount()
				latencySum += h.GetSampleSum()
				for _, bucket := range h.GetBucket() {
					buckets[bucket.GetUpperBound()] += bucket.GetCumulativeCount()
				}
			}
		}
	}

	if out.RequestsTotal > 0 {
		out.ErrorRate = float64(errCount) / float64(out.RequestsTotal)
	}
	if latencyCount > 0 {
		out.LatencyMS.Avg = latencySum / float64(latencyCount) * 1000
		out.LatencyMS.P50 = percentile(buckets, latencyCount, 0.50) * 1000
		out.LatencyMS.P95 = percentile(buckets, latencyCount, 0.95) * 1000
	}
	return out
}

func percentile(buckets map[float64]uint64, count uint64, p float64) float64 {
	if count == 0 {
		return 0
	}
	bounds := make([]float64, 0, len(buckets))
	for bound := range buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)

	target := p * float64(count)
	var prevBound float64
	var prevCum uint64
	for _, bound := range bounds {
		cum := buckets[bound]
		if float64(cum) >= target {
			if math.IsInf(bound, 1) || cum == prevCum {
				return prevBound
			}
			frac := (target - float64(prevCum)) / float64(cum-prevCum)
			return prevBound + frac*(bound-prevBound)
		}
		prevBound, prevCum = bound, cum
	}
	return bounds[len(bounds)-1]
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/metrics/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/metrics
git commit -m "feat(metrics): expose in-process snapshot for dashboard"
```

---

## Task 3: Summary endpoint

**Files:**
- Create: `internal/api/summary.go`
- Test: `internal/api/admin_api_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/api/admin_api_test.go`:

```go
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
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/api/ -run TestSummary -v`

Expected: `registerSummary` undefined.

- [ ] **Step 3: Implement `internal/api/summary.go`**

```go
package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

type budgetStatus struct {
	Limit     float64 `json:"limit"`
	SoftRatio float64 `json:"soft_ratio"`
	Cost      float64 `json:"cost"`
	Saved     float64 `json:"saved"`
	UsedRatio float64 `json:"used_ratio"`
	Degraded  bool    `json:"degraded"`
	Rejected  bool    `json:"rejected"`
}

type summaryResponse struct {
	usage.Summary
	Budget budgetStatus `json:"budget"`
}

func registerSummary(v1 *gin.RouterGroup, repo usage.Repo, s config.Settings) {
	v1.GET("/admin/summary", func(c *gin.Context) {
		month := c.DefaultQuery("month", usage.CurrentMonth())
		if _, err := time.Parse("2006-01", month); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid month, want YYYY-MM"}})
			return
		}

		summary, err := repo.Summary(c.Request.Context(), month)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		cost, _ := repo.MonthlyCost(c.Request.Context(), month)
		saved, _ := repo.MonthlySaved(c.Request.Context(), month)

		budget := budgetStatus{
			Limit:     s.BudgetMonthlyLimit,
			SoftRatio: s.BudgetSoftRatio,
			Cost:      cost,
			Saved:     saved,
		}
		if budget.Limit > 0 {
			budget.UsedRatio = cost / budget.Limit
			budget.Degraded = cost >= budget.Limit*budget.SoftRatio && cost < budget.Limit
			budget.Rejected = cost >= budget.Limit
		}
		c.JSON(http.StatusOK, summaryResponse{Summary: summary, Budget: budget})
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -run TestSummary -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/summary.go internal/api/admin_api_test.go
git commit -m "feat(api): add admin summary endpoint"
```

---

## Task 4: Requests endpoint

**Files:**
- Create: `internal/api/requests.go`
- Test: `internal/api/admin_api_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/api/admin_api_test.go`:

```go
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
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/api/ -run TestRequests -v`

Expected: `registerRequests` undefined.

- [ ] **Step 3: Implement `internal/api/requests.go`**

```go
package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/usage"
)

func registerRequests(v1 *gin.RouterGroup, repo usage.Repo) {
	v1.GET("/admin/requests", func(c *gin.Context) {
		q := usage.Query{
			Model:    c.Query("model"),
			Provider: c.Query("provider"),
			Q:        strings.TrimSpace(c.Query("q")),
		}
		switch c.Query("cache") {
		case "", "all":
		case "hit":
			v := true
			q.Cache = &v
		case "miss":
			v := false
			q.Cache = &v
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "cache must be hit, miss or all"}})
			return
		}

		limit, err := intParam(c.Query("limit"), 50, 1, 200)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "limit must be a number between 1 and 200"}})
			return
		}
		offset, err := intParam(c.Query("offset"), 0, 0, 1<<30)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "offset must be a non-negative number"}})
			return
		}
		q.Limit = limit
		q.Offset = offset

		total, records, err := repo.Query(c.Request.Context(), q)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"total": total, "records": records})
	})
}

func intParam(raw string, def, min, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -run TestRequests -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/requests.go internal/api/admin_api_test.go
git commit -m "feat(api): add filterable paginated requests endpoint"
```

---

## Task 5: Health endpoint

**Files:**
- Create: `internal/api/health.go`
- Test: `internal/api/admin_api_test.go`

- [ ] **Step 1: Write failing test**

Append:

```go
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
```

Add `"llm-gateway/internal/metrics"` to the test imports.

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/api/ -run TestHealth -v`

Expected: `registerHealth` undefined.

- [ ] **Step 3: Implement `internal/api/health.go`**

```go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/metrics"
)

func registerHealth(v1 *gin.RouterGroup, m *metrics.Registry) {
	v1.GET("/admin/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, m.Snapshot())
	})
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -run TestHealth -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/health.go internal/api/admin_api_test.go
git commit -m "feat(api): add runtime health endpoint"
```

---

## Task 6: Wire routes into the router

**Files:**
- Modify: `internal/api/router.go`
- Test: `internal/api/admin_api_test.go`

- [ ] **Step 1: Write failing test**

Append:

```go
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
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/api/ -run TestAdminEndpoints -v`

Expected: 404 for the new paths.

- [ ] **Step 3: Wire routes**

In `internal/api/router.go`, immediately after the usage repo and metrics registry are created (variables `repo` and `m`) and the `v1` group exists, add:

```go
	registerSummary(v1, repo, s)
	registerRequests(v1, repo)
	registerHealth(v1, m)
```

Place these next to the existing `registerAdmin(v1, repo)` / `registerPricing(v1, s)` calls so all admin routes live together.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/router.go internal/api/admin_api_test.go
git commit -m "feat(api): wire summary, requests and health routes"
```

---

## Task 7: Vendor Chart.js and serve it locally

**Files:**
- Create: `internal/api/web/chart.umd.min.js`
- Modify: `internal/api/dashboard.go`
- Test: `internal/api/dashboard_test.go`

- [ ] **Step 1: Download the vendored asset**

Run (retry with proxy if it fails):

```powershell
curl.exe -L --retry 3 -o internal/api/web/chart.umd.min.js https://cdn.jsdelivr.net/npm/chart.js@4.4.9/dist/chart.umd.min.js
```

If that fails, retry through the local proxy:

```powershell
curl.exe -L --retry 3 -x http://127.0.0.1:7890 -o internal/api/web/chart.umd.min.js https://cdn.jsdelivr.net/npm/chart.js@4.4.9/dist/chart.umd.min.js
```

Verify:

```powershell
(Get-Item internal/api/web/chart.umd.min.js).Length
```

Expected: > 100000 bytes.

- [ ] **Step 2: Write failing tests**

Append to `internal/api/dashboard_test.go`:

```go
func TestDashboardServesChartJS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/chart.js", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if w.Body.Len() < 100000 {
		t.Fatalf("chart.js looks truncated: %d bytes", w.Body.Len())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("content type: %q", ct)
	}
}

func TestDashboardHasNoExternalAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	for _, bad := range []string{"cdn.jsdelivr.net", "unpkg.com", "cdnjs.cloudflare.com"} {
		if strings.Contains(body, bad) {
			t.Fatalf("dashboard must not depend on %s", bad)
		}
	}
	if !strings.Contains(body, "/assets/chart.js") {
		t.Fatal("dashboard should load the vendored chart.js")
	}
}
```

- [ ] **Step 3: Implement in `internal/api/dashboard.go`**

Keep the existing `//go:embed web/index.html` declaration and add below it:

```go
//go:embed web/chart.umd.min.js
var chartJS []byte
```

Then add this route inside `registerDashboard`, after the `/` handler:

```go
	r.GET("/assets/chart.js", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", chartJS)
	})
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/api/ -run TestDashboardServesChartJS -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/web/chart.umd.min.js internal/api/dashboard.go internal/api/dashboard_test.go
git commit -m "feat(dashboard): vendor chart.js and serve it locally"
```

---

## Task 8: Rewrite the dashboard page

**Files:**
- Modify: `internal/api/web/index.html`

- [ ] **Step 1: Replace the file shell**

Write this complete shell (it intentionally has no external URLs, no `toFixed(3)`, and keeps `id="records"`):

```html
<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>LLM Gateway 用量看板</title>
<script src="/assets/chart.js"></script>
<style>
  :root { --bg:#f6f7f9; --card:#fff; --line:#e5e7eb; --muted:#6b7280; --accent:#2563eb; --warn:#d97706; --bad:#dc2626; --good:#16a34a; }
  * { box-sizing: border-box; }
  body { font-family: system-ui, "Microsoft YaHei", sans-serif; margin: 0; background: var(--bg); color:#111827; }
  header { display:flex; align-items:center; gap:16px; padding:16px 24px; background:#fff; border-bottom:1px solid var(--line); }
  header h1 { font-size:18px; margin:0; }
  .spacer { flex:1; }
  .muted { color: var(--muted); font-size:12px; }
  main { padding: 20px 24px 48px; display:flex; flex-direction:column; gap:16px; }
  .kpis { display:grid; grid-template-columns:repeat(auto-fit,minmax(160px,1fr)); gap:12px; }
  .card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:14px 16px; }
  .kpi .label { color:var(--muted); font-size:12px; }
  .kpi .value { font-size:22px; font-weight:600; margin-top:6px; font-variant-numeric: tabular-nums; }
  .budget-bar { height:10px; background:#e5e7eb; border-radius:999px; overflow:hidden; margin-top:8px; }
  .budget-fill { height:100%; width:0; background:var(--good); transition:width .3s; }
  .budget-fill.warn { background:var(--warn); }
  .budget-fill.bad { background:var(--bad); }
  .grid2 { display:grid; grid-template-columns:2fr 1fr; gap:16px; }
  @media (max-width: 900px){ .grid2 { grid-template-columns:1fr; } }
  .card h2 { font-size:14px; margin:0 0 10px; }
  .chart-wrap { position:relative; height:260px; }
  canvas { max-width:100%; }
  .row { display:flex; gap:8px; flex-wrap:wrap; align-items:center; }
  button, select, input { font: inherit; padding:6px 10px; border:1px solid var(--line); border-radius:8px; background:#fff; }
  button { cursor:pointer; }
  button.active { background:var(--accent); color:#fff; border-color:var(--accent); }
  button:disabled { opacity:.5; cursor:default; }
  table { border-collapse:collapse; width:100%; font-size:12px; font-family: ui-monospace, Consolas, monospace; }
  th, td { border-bottom:1px solid var(--line); padding:6px 8px; text-align:right; white-space:nowrap; }
  th:nth-child(-n+3), td:nth-child(-n+3) { text-align:left; }
  .dot { display:inline-block; width:8px; height:8px; border-radius:50%; background:#9ca3af; margin-right:6px; }
  .dot.on { background:var(--good); }
  .empty { color:var(--muted); padding:20px; text-align:center; }
  .hidden { display:none !important; }
  #budget-text { margin-bottom:2px; }
</style>
</head>
<body>
<header>
  <h1>LLM Gateway</h1>
  <span id="month-label" class="muted"></span>
  <span class="spacer"></span>
  <span id="conn" class="muted"><span class="dot"></span>未连接</span>
  <span id="updated" class="muted"></span>
  <button onclick="refreshAll()">刷新</button>
</header>
<main>
  <div id="token-bar" class="card">
    <div class="row">
      <input id="token" placeholder="gateway token" style="flex:1;min-width:240px" />
      <button onclick="saveToken()">保存</button>
    </div>
    <div class="muted" style="margin-top:6px">首次填入 token，之后会记住（也可用 /?token=你的token）。</div>
  </div>

  <section class="kpis">
    <div class="card kpi"><div class="label">本月花费（元）</div><div class="value" id="k-cost">-</div></div>
    <div class="card kpi"><div class="label">本月节省（元）</div><div class="value" id="k-saved">-</div></div>
    <div class="card kpi"><div class="label">本月请求数</div><div class="value" id="k-req">-</div></div>
    <div class="card kpi"><div class="label">缓存命中率</div><div class="value" id="k-cache">-</div></div>
    <div class="card kpi"><div class="label">本月 tokens</div><div class="value" id="k-tokens">-</div></div>
  </section>

  <section class="card">
    <h2>预算</h2>
    <div id="budget-text" class="muted">-</div>
    <div class="budget-bar"><div id="budget-fill" class="budget-fill"></div></div>
  </section>

  <section class="grid2">
    <div class="card">
      <h2>趋势
        <span class="row" style="float:right">
          <button id="g-day" class="active" onclick="setGranularity('day')">按天</button>
          <button id="g-hour" onclick="setGranularity('hour')">按小时</button>
        </span>
      </h2>
      <div class="row" id="range-row" style="margin-bottom:8px">
        <button data-range="today" onclick="setRange('today')">今日</button>
        <button data-range="7d" onclick="setRange('7d')">近7天</button>
        <button data-range="30d" class="active" onclick="setRange('30d')">近30天</button>
        <button data-range="month" onclick="setRange('month')">本月</button>
      </div>
      <div class="chart-wrap"><canvas id="trend"></canvas></div>
    </div>
    <div class="card">
      <h2>模型成本占比</h2>
      <div class="chart-wrap"><canvas id="models"></canvas></div>
    </div>
  </section>

  <section class="grid2">
    <div class="card">
      <h2>供应商</h2>
      <table id="providers"><thead><tr><th>供应商</th><th>请求</th><th>成本</th><th>占比</th></tr></thead><tbody></tbody></table>
    </div>
    <div class="card">
      <h2>运行健康</h2>
      <div class="row" style="gap:24px">
        <div><div class="muted">请求总数</div><div class="value" id="h-req">-</div></div>
        <div><div class="muted">错误率</div><div class="value" id="h-err">-</div></div>
        <div><div class="muted">P50</div><div class="value" id="h-p50">-</div></div>
        <div><div class="muted">P95</div><div class="value" id="h-p95">-</div></div>
      </div>
      <table id="status" style="margin-top:12px"><thead><tr><th>供应商</th><th>状态</th><th>次数</th></tr></thead><tbody></tbody></table>
    </div>
  </section>

  <section class="card">
    <h2>调用明细</h2>
    <div class="row" style="margin-bottom:10px">
      <select id="f-provider" onchange="applyFilters()"><option value="">全部供应商</option></select>
      <select id="f-model" onchange="applyFilters()"><option value="">全部模型</option></select>
      <select id="f-cache" onchange="applyFilters()"><option value="">全部缓存</option><option value="hit">命中</option><option value="miss">未命中</option></select>
      <input id="f-q" placeholder="搜索 id / 模型" onkeydown="if(event.key==='Enter')applyFilters()" />
      <span class="spacer"></span>
      <span id="page-info" class="muted"></span>
      <button id="prev" onclick="prevPage()">上一页</button>
      <button id="next" onclick="nextPage()">下一页</button>
    </div>
    <table id="records">
      <thead><tr><th>时间</th><th>模型</th><th>供应商</th><th>prompt</th><th>completion</th><th>成本(元)</th><th>节省(元)</th><th>缓存</th></tr></thead>
      <tbody></tbody>
    </table>
    <div id="records-empty" class="empty hidden">暂无数据</div>
  </section>
</main>
<script>
// SCRIPT_PLACEHOLDER
</script>
</body>
</html>
```

- [ ] **Step 2: Replace `// SCRIPT_PLACEHOLDER` with the dashboard script**

See the next task block for the complete script; insert it verbatim in place of the placeholder.

- [ ] **Step 3: Insert this script (replacing `// SCRIPT_PLACEHOLDER`)**

```js
const state = {
  token: '', granularity: 'day', range: '30d',
  page: 0, pageSize: 50, total: 0,
  filters: { provider: '', model: '', cache: '', q: '' },
  filtersReady: false,
  summary: null, records: [],
  trend: null, modelsChart: null,
  streamAbort: null, refreshTimer: null, debounceTimer: null,
};

const $ = (id) => document.getElementById(id);
const fmtInt = (v) => (v || 0).toLocaleString('zh-CN');
const fmtCost = (v) => (v || 0).toFixed(6);
const pct = (v) => ((v || 0) * 100).toFixed(2) + '%';
const fmtTime = (v) => {
  const d = new Date(v);
  if (isNaN(d.getTime())) return v || '';
  const pad = (n) => String(n).padStart(2, '0');
  return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' +
    pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
};

function initToken() {
  const params = new URLSearchParams(location.search);
  const fromUrl = params.get('token');
  if (fromUrl) {
    localStorage.setItem('gateway_token', fromUrl);
    params.delete('token');
    const rest = params.toString();
    history.replaceState(null, '', location.pathname + (rest ? '?' + rest : ''));
  }
  state.token = localStorage.getItem('gateway_token') || '';
  $('token').value = state.token;
  updateTokenBar();
}

function saveToken() {
  state.token = $('token').value.trim();
  localStorage.setItem('gateway_token', state.token);
  updateTokenBar();
  connectStream();
  refreshAll();
}

function updateTokenBar() {
  $('token-bar').classList.toggle('hidden', !!state.token);
}

async function api(path) {
  const res = await fetch(path, { headers: { Authorization: 'Bearer ' + state.token } });
  if (res.status === 401) {
    state.token = '';
    updateTokenBar();
    throw new Error('token 无效');
  }
  if (!res.ok) throw new Error('HTTP ' + res.status);
  return res.json();
}

function showError(err) {
  $('updated').textContent = '加载失败: ' + err.message;
}

function dayCount() {
  if (state.range === 'today') return 1;
  if (state.range === '7d') return 7;
  if (state.range === '30d') return 30;
  return new Date().getDate();
}

function lastDays(n) {
  const out = [];
  const now = new Date();
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i);
    out.push(d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0'));
  }
  return out;
}

function lastHours(n) {
  const out = [];
  const now = new Date();
  now.setMinutes(0, 0, 0);
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(now.getTime() - i * 3600 * 1000);
    out.push(
      d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0') +
      ' ' + String(d.getHours()).padStart(2, '0') + ':00'
    );
  }
  return out;
}

function indexBy(series, key) {
  const map = {};
  (series || []).forEach((item) => { map[item[key]] = item; });
  return map;
}

function renderKpis(sum) {
  const t = sum.totals || {};
  $('month-label').textContent = sum.month || '';
  $('k-cost').textContent = '¥' + fmtCost(t.cost);
  $('k-saved').textContent = '¥' + fmtCost(t.saved);
  $('k-req').textContent = fmtInt(t.requests);
  $('k-cache').textContent = pct(t.cache_hit_ratio);
  $('k-tokens').textContent = fmtInt((t.prompt_tokens || 0) + (t.completion_tokens || 0));
}

function renderBudget(b) {
  if (!b) return;
  const text = $('budget-text');
  const fill = $('budget-fill');
  if (!b.limit || b.limit <= 0) {
    text.textContent = '未设置预算（BUDGET_MONTHLY_LIMIT=0），当前花费 ¥' + fmtCost(b.cost) + '，节省 ¥' + fmtCost(b.saved);
    fill.style.width = '0%';
    fill.className = 'budget-fill';
    return;
  }
  const ratio = Math.min(1, b.used_ratio || 0);
  fill.style.width = (ratio * 100).toFixed(1) + '%';
  fill.className = 'budget-fill' + (b.rejected ? ' bad' : b.degraded ? ' warn' : '');
  text.textContent = '已用 ' + pct(b.used_ratio) + '（¥' + fmtCost(b.cost) + ' / ¥' + fmtCost(b.limit) + '）' +
    (b.rejected ? ' · 已超预算，请求会被拒绝' : b.degraded ? ' · 已进入降级区' : '');
}

function renderTrend() {
  if (typeof Chart === 'undefined' || !state.summary) return;
  const byDay = indexBy(state.summary.by_day, 'day');
  const byHour = indexBy(state.summary.by_hour, 'hour');
  let keys;
  let rows;
  if (state.granularity === 'hour') {
    keys = lastHours(48);
    rows = keys.map((k) => byHour[k] || {});
  } else {
    keys = lastDays(dayCount());
    rows = keys.map((k) => byDay[k] || {});
  }
  const labels = keys.map((k) => (state.granularity === 'hour' ? k.slice(5, 16) : k.slice(5)));
  const requests = rows.map((r) => r.requests || 0);
  const costs = rows.map((r) => r.cost || 0);

  const data = {
    labels,
    datasets: [
      { type: 'bar', label: '请求数', data: requests, backgroundColor: 'rgba(37,99,235,0.55)', borderRadius: 4, yAxisID: 'y' },
      { type: 'line', label: '成本(元)', data: costs, borderColor: '#16a34a', backgroundColor: '#16a34a', tension: 0.25, pointRadius: 2, yAxisID: 'y1' },
    ],
  };
  if (!state.trend) {
    state.trend = new Chart($('trend'), {
      type: 'bar',
      data,
      options: {
        responsive: true, maintainAspectRatio: false, interaction: { mode: 'index', intersect: false },
        plugins: { legend: { position: 'bottom' } },
        scales: {
          y: { beginAtZero: true, ticks: { precision: 0 }, title: { display: true, text: '请求数' } },
          y1: { beginAtZero: true, position: 'right', grid: { drawOnChartArea: false }, title: { display: true, text: '成本(元)' } },
        },
      },
    });
  } else {
    state.trend.data = data;
    state.trend.update();
  }
}

function renderModels() {
  if (typeof Chart === 'undefined' || !state.summary) return;
  const items = state.summary.by_model || [];
  const byCost = items.reduce((acc, m) => acc + (m.cost || 0), 0) > 0;
  const labels = items.map((m) => m.model + (byCost ? ' (' + fmtCost(m.cost) + ')' : ''));
  const values = items.map((m) => (byCost ? m.cost : m.requests));
  const colors = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#db2777', '#65a30d'];
  const data = {
    labels,
    datasets: [{ data: values, backgroundColor: colors, borderWidth: 1 }],
  };
  if (!state.modelsChart) {
    state.modelsChart = new Chart($('models'), {
      type: 'doughnut',
      data,
      options: {
        responsive: true, maintainAspectRatio: false,
        plugins: { legend: { position: 'right', labels: { boxWidth: 10 } } },
      },
    });
  } else {
    state.modelsChart.data = data;
    state.modelsChart.update();
  }
}

function renderProviders(list) {
  const tbody = document.querySelector('#providers tbody');
  tbody.innerHTML = '';
  const totalCost = (list || []).reduce((acc, p) => acc + (p.cost || 0), 0);
  (list || []).forEach((p) => {
    const tr = document.createElement('tr');
    [p.provider, fmtInt(p.requests), fmtCost(p.cost), totalCost > 0 ? pct(p.cost / totalCost) : '0.00%'].forEach((v) => {
      const td = document.createElement('td');
      td.textContent = v;
      tr.appendChild(td);
    });
    tbody.appendChild(tr);
  });
}

function fillFilters(sum) {
  const provider = $('f-provider');
  const model = $('f-model');
  (sum.by_provider || []).forEach((p) => {
    const option = document.createElement('option');
    option.value = p.provider;
    option.textContent = p.provider;
    provider.appendChild(option);
  });
  (sum.by_model || []).forEach((m) => {
    const option = document.createElement('option');
    option.value = m.model;
    option.textContent = m.model;
    model.appendChild(option);
  });
}

async function loadSummary() {
  const sum = await api('/v1/admin/summary');
  state.summary = sum;
  renderKpis(sum);
  renderBudget(sum.budget);
  renderTrend();
  renderModels();
  renderProviders(sum.by_provider);
  if (!state.filtersReady) {
    fillFilters(sum);
    state.filtersReady = true;
  }
}

async function loadHealth() {
  const h = await api('/v1/admin/health');
  const latency = h.latency_ms || {};
  $('h-req').textContent = fmtInt(h.requests_total);
  $('h-err').textContent = pct(h.error_rate);
  $('h-p50').textContent = (latency.p50 || 0).toFixed(0) + ' ms';
  $('h-p95').textContent = (latency.p95 || 0).toFixed(0) + ' ms';
  const tbody = document.querySelector('#status tbody');
  tbody.innerHTML = '';
  (h.by_status || []).slice().sort((a, b) => b.count - a.count).forEach((s) => {
    const tr = document.createElement('tr');
    [s.provider, s.status, fmtInt(s.count)].forEach((v) => {
      const td = document.createElement('td');
      td.textContent = v;
      tr.appendChild(td);
    });
    tbody.appendChild(tr);
  });
}

function recordParams() {
  const params = new URLSearchParams();
  if (state.filters.provider) params.set('provider', state.filters.provider);
  if (state.filters.model) params.set('model', state.filters.model);
  if (state.filters.cache) params.set('cache', state.filters.cache);
  if (state.filters.q) params.set('q', state.filters.q);
  params.set('limit', state.pageSize);
  params.set('offset', state.page * state.pageSize);
  return params;
}

async function loadRecords() {
  const data = await api('/v1/admin/requests?' + recordParams().toString());
  state.total = data.total || 0;
  state.records = data.records || [];
  renderRecords();
}

function renderRecords() {
  const tbody = document.querySelector('#records tbody');
  tbody.innerHTML = '';
  $('records-empty').classList.toggle('hidden', state.records.length > 0);
  state.records.forEach((r) => {
    const tr = document.createElement('tr');
    [fmtTime(r.created_at), r.model, r.provider, fmtInt(r.prompt_tokens), fmtInt(r.completion_tokens), fmtCost(r.cost), fmtCost(r.saved_cost)].forEach((v) => {
      const td = document.createElement('td');
      td.textContent = v;
      tr.appendChild(td);
    });
    const td = document.createElement('td');
    const dot = document.createElement('span');
    dot.className = 'dot' + (r.cache_hit ? ' on' : '');
    td.appendChild(dot);
    td.appendChild(document.createTextNode(r.cache_hit ? '命中' : '未命中'));
    tr.appendChild(td);
    tbody.appendChild(tr);
  });
  const pages = Math.max(1, Math.ceil(state.total / state.pageSize));
  $('page-info').textContent = '共 ' + fmtInt(state.total) + ' 条 · 第 ' + (state.page + 1) + '/' + pages + ' 页';
  $('prev').disabled = state.page <= 0;
  $('next').disabled = (state.page + 1) * state.pageSize >= state.total;
}

function applyFilters() {
  state.filters.provider = $('f-provider').value;
  state.filters.model = $('f-model').value;
  state.filters.cache = $('f-cache').value;
  state.filters.q = $('f-q').value.trim();
  state.page = 0;
  loadRecords().catch(showError);
}

function prevPage() {
  if (state.page > 0) {
    state.page--;
    loadRecords().catch(showError);
  }
}

function nextPage() {
  if ((state.page + 1) * state.pageSize < state.total) {
    state.page++;
    loadRecords().catch(showError);
  }
}

function setGranularity(granularity) {
  state.granularity = granularity;
  $('g-day').classList.toggle('active', granularity === 'day');
  $('g-hour').classList.toggle('active', granularity === 'hour');
  $('range-row').classList.toggle('hidden', granularity === 'hour');
  renderTrend();
}

function setRange(range) {
  state.range = range;
  document.querySelectorAll('[data-range]').forEach((button) => {
    button.classList.toggle('active', button.dataset.range === range);
  });
  renderTrend();
}

function setConn(ok) {
  const el = $('conn');
  el.innerHTML = '';
  const dot = document.createElement('span');
  dot.className = 'dot' + (ok ? ' on' : '');
  el.appendChild(dot);
  el.appendChild(document.createTextNode(ok ? '实时' : '未连接'));
}

function matchesFilters(r) {
  const f = state.filters;
  if (f.provider && r.provider !== f.provider) return false;
  if (f.model && r.model !== f.model) return false;
  if (f.cache === 'hit' && !r.cache_hit) return false;
  if (f.cache === 'miss' && r.cache_hit) return false;
  if (f.q && (r.id || '').indexOf(f.q) < 0 && (r.model || '').indexOf(f.q) < 0) return false;
  return true;
}

function scheduleRefresh() {
  if (state.debounceTimer) clearTimeout(state.debounceTimer);
  state.debounceTimer = setTimeout(() => {
    Promise.all([loadSummary(), loadHealth(), loadRecords()]).catch(showError);
  }, 800);
}

function onStreamRecord(record) {
  if (state.page === 0 && matchesFilters(record)) {
    state.records.unshift(record);
    state.total++;
    state.records = state.records.slice(0, state.pageSize);
    renderRecords();
  }
  scheduleRefresh();
}

function scheduleReconnect() {
  if (state.refreshTimer) clearTimeout(state.refreshTimer);
  state.refreshTimer = setTimeout(connectStream, 3000);
}

function connectStream() {
  if (state.streamAbort) state.streamAbort.abort();
  if (!state.token) return;
  const ctrl = new AbortController();
  state.streamAbort = ctrl;
  setConn(false);

  fetch('/v1/admin/usage/stream', {
    headers: { Authorization: 'Bearer ' + state.token },
    signal: ctrl.signal,
  }).then(async (res) => {
    if (res.status === 401) {
      state.token = '';
      updateTokenBar();
      throw new Error('unauthorized');
    }
    if (!res.ok || !res.body) throw new Error('stream HTTP ' + res.status);
    setConn(true);
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      buffer += decoder.decode(chunk.value, { stream: true });
      let split;
      while ((split = buffer.indexOf('\n\n')) >= 0) {
        const event = buffer.slice(0, split);
        buffer = buffer.slice(split + 2);
        event.split('\n').forEach((line) => {
          if (line.indexOf('data:') !== 0) return;
          const payload = line.slice(5).trim();
          if (!payload) return;
          try { onStreamRecord(JSON.parse(payload)); } catch (_) {}
        });
      }
    }
    throw new Error('stream closed');
  }).catch(() => {
    if (ctrl.signal.aborted || !state.token) return;
    setConn(false);
    scheduleReconnect();
  });
}

function refreshAll() {
  if (!state.token) return Promise.resolve();
  return Promise.all([loadSummary(), loadHealth(), loadRecords()])
    .then(() => {
      $('updated').textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN', { hour12: false });
    })
    .catch(showError);
}

initToken();
setGranularity('day');
setRange('30d');
if (state.token) {
  refreshAll();
  connectStream();
  setInterval(() => { if (state.token) refreshAll(); }, 60000);
}
```

- [ ] **Step 4: Run dashboard tests**

Run: `go test ./internal/api/ -run TestDashboard -v`

Expected: PASS, including `TestDashboardServesChartJS` and `TestDashboardHasNoExternalAssets`.

- [ ] **Step 5: Commit**

```powershell
git add internal/api/web/index.html
git commit -m "feat(dashboard): cost, budget, health, trends and request filters"
```

---

## Task 9: Full verification

- [ ] **Step 1: Static checks**

```powershell
go vet ./...
go test ./...
go build -o bin/gateway.exe ./cmd/server
```

Expected: no vet errors, all tests PASS, build succeeds.

- [ ] **Step 2: Restart the gateway**

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\stop.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\start.ps1
```

- [ ] **Step 3: Verify endpoints**

```powershell
$tok = (Select-String -Path .env -Pattern '^GATEWAY_TOKEN=(.+)$').Matches.Groups[1].Value
curl.exe -s http://localhost:8080/healthz
curl.exe -s -H "Authorization: Bearer $tok" http://localhost:8080/v1/admin/summary
curl.exe -s -H "Authorization: Bearer $tok" "http://localhost:8080/v1/admin/requests?limit=3"
curl.exe -s -H "Authorization: Bearer $tok" http://localhost:8080/v1/admin/health
```

Expected:
- summary returns `month`, `totals`, `budget`, `by_model`, `by_provider`, `by_day`, `by_hour`
- requests returns `total` and `records` (newest first)
- health returns `requests_total`, `error_rate`, `latency_ms`, `by_status`
- without the header all three return 401

- [ ] **Step 4: Verify the page has no external dependencies**

```powershell
curl.exe -s http://localhost:8080/ | Select-String -Pattern 'cdn|unpkg|https://'
curl.exe -s -o NUL -w "%{http_code} %{size_download}" http://localhost:8080/assets/chart.js
```

Expected: no matches for the first command; `200 200000`-ish for the second.

- [ ] **Step 5: Manual browser checklist**

Open `http://localhost:8080/?token=<gateway token>` and confirm:
- KPIs show month cost/saved/requests/cache ratio/tokens
- budget bar reflects the disabled budget (`BUDGET_MONTHLY_LIMIT=0`) without errors
- trend toggles between 按天 and 按小时; range buttons change the day window
- model doughnut and provider table are populated
- health card shows totals, error rate, P50/P95, status table
- filters (provider/model/cache/search) and pagination change the table
- sending a chat request updates the KPI within ~1s via SSE; connection dot shows 实时

- [ ] **Step 6: Final commit**

```powershell
git add -A
git commit -m "chore: rebuild gateway with optimized dashboard"
```

---

## Acceptance Criteria

- [ ] `go vet ./...`, `go test ./...`, `go build ./...` all pass.
- [ ] `/v1/admin/summary?month=YYYY-MM` returns budget + totals + by_model/by_provider/by_day/by_hour and rejects malformed months.
- [ ] `/v1/admin/requests` supports `model`, `provider`, `cache=hit|miss`, `q`, `limit` (1–200), `offset`, returns `{total, records}` newest-first.
- [ ] `/v1/admin/health` reports totals, error rate, avg/P50/P95 latency, status breakdown.
- [ ] Dashboard renders without internet access; Chart.js served from `/assets/chart.js`.
- [ ] Real-time updates still work through the SSE stream, with a 60s reconciliation poll as fallback.