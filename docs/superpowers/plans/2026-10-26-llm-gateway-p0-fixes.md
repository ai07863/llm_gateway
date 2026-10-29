# llm-gateway P0 缺陷修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复三处 P0 正确性缺陷——缓存命中不再"隐身"（落库命中与节省金额）、降级只在存在更便宜的已配置模型时发生、故障转移按错误可重试性分类并做有界指数退避重试且模型感知路由。

**Architecture:** 保持现有分层（config → provider → proxy → cache/usage/budget/pricing → api）。在 provider 层引入类型化错误 `provider.Error`；proxy 层改为面向 `Target{Provider, Model}` 的转发并内建重试/退避；cache 条目携带 token 元数据；usage 增 `saved_cost`；budget 降级改为纯函数式选择。

**Tech Stack:** Go 1.26、Gin、modernc.org/sqlite（纯 Go）、Prometheus client。

## Global Constraints

- Go 语言版本：`go 1.26.7`（见 `go.mod`），不新增任何第三方依赖。
- 保持包分层与职责，不引入新的顶层包。
- 所有新增/修改的 Go 文件必须通过 `gofmt`；提交前 `go vet ./...` 与 `go test ./...` 全绿。
- 注释风格：仅在解释"为什么"时写注释，不写"做了什么"的注释（与现有代码一致）。
- 提交信息风格沿用仓库：`fix:` / `feat:` / `improve:` / `docs:`。
- 价格（元/百万 token，`internal/pricing/pricing.go`）：`deepseek-chat = {0.27, 1.10}`，`gpt-4o-mini = {1.10, 4.40}`。价格度量 Score = in + out。

---

### Task 1: provider 类型化错误与可重试性分类

**Files:**
- Create: `internal/provider/errors.go`
- Create: `internal/provider/errors_test.go`
- Modify: `internal/provider/openai_compat.go`（`Chat` 的 `Do`/status 错误、`Embed` 的 `Do`/status 错误、`ChatStream` 的 `Do`/status 错误）
- Modify: `internal/provider/openai_compat_test.go`（新增状态分类用例）

**Interfaces:**
- Produces:
  - `type Error struct { Provider string; StatusCode int; Retryable bool; Err error }`，实现 `Error() string` 与 `Unwrap() error`。
  - `func NewStatusError(providerName string, status int, body string) *Error`
  - `func NewNetworkError(providerName string, err error) *Error`
  - `func IsRetryable(err error) bool`（`*Error` 按其 `Retryable` 字段；其它非 nil 错误返回 true；nil 返回 false）

- [ ] **Step 1: 写失败测试**

Create `internal/provider/errors_test.go`:

```go
package provider

import (
	"errors"
	"testing"
)

func TestIsRetryableClassifiesStatus(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{400, false},
		{401, false},
		{404, false},
	}
	for _, tc := range cases {
		e := NewStatusError("p", tc.status, "body")
		if got := IsRetryable(e); got != tc.want {
			t.Fatalf("status %d: want retryable=%v got %v", tc.status, tc.want, got)
		}
	}
}

func TestIsRetryableUnknownErrorIsRetryable(t *testing.T) {
	if !IsRetryable(errors.New("dial tcp 127.0.0.1: i/o timeout")) {
		t.Fatal("unknown errors should be treated as retryable")
	}
}

func TestIsRetryableNilIsFalse(t *testing.T) {
	if IsRetryable(nil) {
		t.Fatal("nil should not be retryable")
	}
}

func TestNetworkErrorIsRetryable(t *testing.T) {
	e := NewNetworkError("p", errors.New("connection refused"))
	if !IsRetryable(e) {
		t.Fatal("network errors must be retryable")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/provider/ -run TestIsRetryable -v`
Expected: FAIL（`undefined: NewStatusError`，无法编译）

- [ ] **Step 3: 实现 errors.go**

Create `internal/provider/errors.go`:

```go
package provider

import (
	"errors"
	"fmt"
)

type Error struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "provider error"
}

func (e *Error) Unwrap() error { return e.Err }

func NewStatusError(providerName string, status int, body string) *Error {
	return &Error{
		Provider:   providerName,
		StatusCode: status,
		Retryable:  status == 429 || status >= 500,
		Err:        fmt.Errorf("provider %s status %d: %s", providerName, status, body),
	}
}

func NewNetworkError(providerName string, err error) *Error {
	return &Error{Provider: providerName, Retryable: true, Err: err}
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return true
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/provider/ -run TestIsRetryable -v`
Expected: PASS

- [ ] **Step 5: 让 openai_compat 使用类型化错误**

在 `internal/provider/openai_compat.go` 中做三处替换。

`Chat` 内：
```go
	res, err := p.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ChatResponse{}, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}
```

`Embed` 内：
```go
	res, err := p.client.Do(httpReq)
	if err != nil {
		return nil, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, NewStatusError(p.cfg.Name, res.StatusCode, "")
	}
```

`ChatStream` 内：
```go
	res, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, NewNetworkError(p.cfg.Name, err)
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}
```

- [ ] **Step 6: 增加 provider HTTP 状态分类测试**

追加到 `internal/provider/openai_compat_test.go`（保留文件顶部现有 import，新增 `net/http` 已存在；无需新 import）：

```go
func TestOpenAICompatChatStatusErrorIsRetryableFor5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || !IsRetryable(err) {
		t.Fatalf("5xx must be retryable, got %v", err)
	}
}

func TestOpenAICompatChatStatusErrorIsPermanentFor4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	_, err := p.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || IsRetryable(err) {
		t.Fatalf("4xx must be permanent, got %v", err)
	}
}
```

- [ ] **Step 7: 运行 provider 全部测试**

Run: `go test ./internal/provider/ -v`
Expected: PASS（含既有 stream 测试）

- [ ] **Step 8: 提交**

```bash
gofmt -w internal/provider/errors.go internal/provider/errors_test.go internal/provider/openai_compat.go internal/provider/openai_compat_test.go
git add internal/provider/
git commit -m "fix: provider 类型化错误, 按状态码区分可重试性"
```

---

### Task 2: 缓存条目携带 token 元数据

**Files:**
- Modify: `internal/cache/exact.go`
- Modify: `internal/cache/semantic.go`
- Modify: `internal/cache/exact_test.go`
- Modify: `internal/cache/semantic_test.go`
- Modify: `internal/api/router.go`（同步调用点，保证可编译）

**Interfaces:**
- Produces:
  - `type CachedResult struct { Content string; PromptTokens int; CompletionTokens int }`
  - `func (c *Exact) Get(key string) (CachedResult, bool)`
  - `func (c *Exact) Set(key string, res CachedResult)`
  - `func (s *Semantic) Lookup(ctx context.Context, scope, text string) (CachedResult, []float64, bool)`
  - `func (s *Semantic) Store(scope string, qvec []float64, res CachedResult) error`

- [ ] **Step 1: 写失败测试（exact）**

替换 `internal/cache/exact_test.go` 中的调用，并新增 token 用例：

```go
func TestExactCacheHitAndMiss(t *testing.T) {
	c := NewExact(time.Minute)
	req := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	key := c.Key(req)

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss")
	}
	c.Set(key, CachedResult{Content: "answer", PromptTokens: 10, CompletionTokens: 20})
	got, ok := c.Get(key)
	if !ok || got.Content != "answer" || got.PromptTokens != 10 || got.CompletionTokens != 20 {
		t.Fatalf("expected hit with tokens, got %+v %v", got, ok)
	}
}
```

其余测试替换为 `Set`/`Get` 的新签名，例如：
```go
	c.Set(keyZero, CachedResult{Content: "zero-answer"})
	if _, ok := c.Get(keyOne); ok { t.Fatal("...") }
	if got, ok := c.Get(keyZero); !ok || got.Content != "zero-answer" { t.Fatalf("...") }
```
```go
	c.Set(key, CachedResult{Content: "v"})
```
```go
	c.Set("k1", CachedResult{Content: "v1"})
	c.Set("k2", CachedResult{Content: "v2"})
	c.Set("k3", CachedResult{Content: "v3"})
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/cache/ -run TestExact -v`
Expected: FAIL（无法编译：`Set`/`Get` 签名不符）

- [ ] **Step 3: 实现 exact.go**

把 `entry`、`Get`、`Set` 改为：

```go
type CachedResult struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
}

type entry struct {
	res     CachedResult
	expires time.Time
}
```

```go
func (c *Exact) Get(key string) (CachedResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[key]
	if !ok || NowFunc().After(e.expires) {
		return CachedResult{}, false
	}
	return e.res, true
}

func (c *Exact) Set(key string, res CachedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[key]; !exists {
		c.order = append(c.order, key)
	}
	c.m[key] = entry{res: res, expires: NowFunc().Add(c.ttl)}
	c.evictLocked()
}
```

- [ ] **Step 4: 写失败测试（semantic）**

更新 `internal/cache/semantic_test.go` 的 `Store`/`Lookup` 调用：

```go
func TestSemanticLookupHit(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	vecs, _ := fakeEmbedder{}.Embed(ctx, []string{"hello world"})
	if err := s.Store("scope", vecs[0], CachedResult{Content: "answer", PromptTokens: 3, CompletionTokens: 4}); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, _, ok := s.Lookup(ctx, "scope", "hi there")
	if !ok || got.Content != "answer" || got.PromptTokens != 3 || got.CompletionTokens != 4 {
		t.Fatalf("want hit with tokens, got %+v %v", got, ok)
	}
}
```

`TestSemanticScopeIsolation` 与 `TestSemanticStoreEmptyVectorIsNoop` 同样把字符串 answer 改为 `CachedResult{Content: "..."}`，并把 `v, _, ok :=` 后对 `v` 的断言改为 `got.Content`。

- [ ] **Step 5: 运行确认失败**

Run: `go test ./internal/cache/ -run TestSemantic -v`
Expected: FAIL（无法编译）

- [ ] **Step 6: 实现 semantic.go**

```go
type semanticEntry struct {
	scope string
	vec   []float64
	res   CachedResult
}
```

```go
func (s *Semantic) Lookup(ctx context.Context, scope, text string) (CachedResult, []float64, bool) {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return CachedResult{}, nil, false
	}
	q := vecs[0]
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best CachedResult
	bestScore := 0.0
	for _, e := range s.entries {
		if e.scope != scope {
			continue
		}
		if sc := similarity.Cosine(q, e.vec); sc > bestScore {
			bestScore, best = sc, e.res
		}
	}
	if bestScore >= s.threshold {
		return best, q, true
	}
	return CachedResult{}, q, false
}

func (s *Semantic) Store(scope string, qvec []float64, res CachedResult) error {
	if len(qvec) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, semanticEntry{scope: scope, vec: qvec, res: res})
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
	return nil
}
```

- [ ] **Step 7: 同步 router.go 调用点（保持行为，不新增计量）**

非流式命中回退块：
```go
			if cached, ok := exact.Get(key); ok {
				m.Requests.WithLabelValues("cache", "hit").Inc()
				return provider.ChatResponse{Model: req.Model, Content: cached.Content, PromptTokens: cached.PromptTokens, CompletionTokens: cached.CompletionTokens}, nil
			}
```
语义命中块：
```go
				if hit, vec, ok := semantic.Lookup(c.Request.Context(), scope, lastUser); ok {
					m.Requests.WithLabelValues("cache", "hit").Inc()
					return provider.ChatResponse{Model: req.Model, Content: hit.Content, PromptTokens: hit.PromptTokens, CompletionTokens: hit.CompletionTokens}, nil
				} else {
					semanticVec = vec
				}
```
非流式写回：
```go
			exact.Set(key, cache.CachedResult{Content: resp.Content, PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens})
			if semantic != nil {
				_ = semantic.Store(scope, semanticVec, cache.CachedResult{Content: resp.Content, PromptTokens: resp.PromptTokens, CompletionTokens: resp.CompletionTokens})
			}
```
流式命中：
```go
			if cached, ok := exact.Get(key); ok {
				m.Requests.WithLabelValues("cache", "hit").Inc()
				writeSSEChunk(c, cached.Content)
				return nil
			}
```
流式写回：
```go
				exact.Set(key, cache.CachedResult{Content: content.String(), PromptTokens: prompt, CompletionTokens: completion})
```

- [ ] **Step 8: 运行全量测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
gofmt -w internal/cache/ internal/api/router.go
git add internal/cache/ internal/api/router.go
git commit -m "improve: 缓存条目携带 token 元数据"
```

---

### Task 3: 用量记录 saved_cost + MonthlySaved + 旧库迁移

**Files:**
- Modify: `internal/usage/usage.go`
- Modify: `internal/usage/sqlite.go`
- Modify: `internal/usage/sqlite_test.go`
- Modify: `internal/api/admin.go`

**Interfaces:**
- Consumes: 无。
- Produces:
  - `usage.Record` 新增字段 `SavedCost float64`（json `saved_cost`）。
  - `usage.Repo` 新增 `MonthlySaved(ctx context.Context, yyyymm string) (float64, error)`。

- [ ] **Step 1: 写失败测试**

替换/新增 `internal/usage/sqlite_test.go`：

```go
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
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/usage/ -v`
Expected: FAIL（`unknown field SavedCost`、`repo.MonthlySaved undefined`）

- [ ] **Step 3: 扩展 usage.go**

```go
type Record struct {
	ID               string    `json:"id"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Cost             float64   `json:"cost"`
	SavedCost        float64   `json:"saved_cost"`
	CacheHit         bool      `json:"cache_hit"`
	CreatedAt        time.Time `json:"created_at"`
}

type Repo interface {
	Record(ctx context.Context, r Record) error
	MonthlyCost(ctx context.Context, yyyymm string) (float64, error)
	MonthlySaved(ctx context.Context, yyyymm string) (float64, error)
	List(ctx context.Context, limit int) ([]Record, error)
	Close() error
}
```

- [ ] **Step 4: 扩展 sqlite.go**

`NewSQLite` 中建表语句增加列，并在建表后做幂等迁移：

```go
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
```

`Record` 插入语句改为：
```go
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO usage_records (id,model,provider,prompt_tokens,completion_tokens,cost,saved_cost,cache_hit,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Model, rec.Provider, rec.PromptTokens, rec.CompletionTokens, rec.Cost, rec.SavedCost, boolToInt(rec.CacheHit), rec.CreatedAt.Format("2006-01-02 15:04:05"))
	return err
```

新增方法：
```go
func (r *sqliteRepo) MonthlySaved(ctx context.Context, yyyymm string) (float64, error) {
	var total sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		`SELECT SUM(saved_cost) FROM usage_records WHERE substr(created_at,1,7) = ?`, yyyymm).Scan(&total)
	return total.Float64, err
}
```

`List` 的 SELECT 与 Scan 增加 saved_cost：
```go
	rows, err := r.db.QueryContext(ctx,
		`SELECT id,model,provider,prompt_tokens,completion_tokens,cost,saved_cost,cache_hit,created_at
		 FROM usage_records ORDER BY created_at DESC LIMIT ?`, limit)
```
```go
		if err := rows.Scan(&rec.ID, &rec.Model, &rec.Provider, &rec.PromptTokens, &rec.CompletionTokens, &rec.Cost, &rec.SavedCost, &hit, &ts); err != nil {
```

- [ ] **Step 5: 运行 usage 测试**

Run: `go test ./internal/usage/ -v`
Expected: PASS

- [ ] **Step 6: admin 响应加 month_saved**

`internal/api/admin.go` 改为：
```go
		month, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
		saved, _ := repo.MonthlySaved(c.Request.Context(), usage.CurrentMonth())
		c.JSON(http.StatusOK, gin.H{"month_cost": month, "month_saved": saved, "records": records})
```

- [ ] **Step 7: 运行全量测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
gofmt -w internal/usage/ internal/api/admin.go
git add internal/usage/ internal/api/admin.go
git commit -m "feat: 用量记录 saved_cost, MonthlySaved 与旧库迁移"
```

---

### Task 4: 降级基于"已配置的便宜模型"

**Files:**
- Modify: `internal/budget/budget.go`
- Modify: `internal/budget/budget_test.go`
- Modify: `internal/pricing/pricing.go`（新增 `Score`）
- Modify: `internal/api/router.go`（构造候选 + 降级调用）
- Modify: `internal/api/budget_test.go`（降级用例改双 provider）
- Modify: `internal/api/stream_test.go`（流式降级用例改双 provider）

**Interfaces:**
- Consumes: `pricing.Score(model) (float64, bool)`。
- Produces:
  - `type budget.Model struct { Name string; Price float64 }`
  - `func budget.CheaperModel(current string, currentPrice float64, candidates []Model) string`
  - `func pricing.Score(model string) (float64, bool)`（返回 in+out 元/百万 token）

- [ ] **Step 1: 写失败测试（budget + pricing）**

替换 `internal/budget/budget_test.go` 的 `TestCheaperModel`：

```go
func TestCheaperModelPicksCheapestConfigured(t *testing.T) {
	cands := []Model{{Name: "deepseek-chat", Price: 1.37}, {Name: "gpt-4o-mini", Price: 5.5}}
	if got := CheaperModel("gpt-4o-mini", 5.5, cands); got != "deepseek-chat" {
		t.Fatalf("want deepseek-chat, got %q", got)
	}
	if got := CheaperModel("deepseek-chat", 1.37, cands); got != "deepseek-chat" {
		t.Fatalf("already cheapest stays same, got %q", got)
	}
}

func TestCheaperModelUnknownCurrentDoesNotDegrade(t *testing.T) {
	cands := []Model{{Name: "deepseek-chat", Price: 1.37}}
	if got := CheaperModel("mystery", 0, cands); got != "mystery" {
		t.Fatalf("unknown current price must not degrade, got %q", got)
	}
}

func TestCheaperModelNoCheaperCandidateStays(t *testing.T) {
	cands := []Model{{Name: "gpt-4o-mini", Price: 5.5}}
	if got := CheaperModel("gpt-4o-mini", 5.5, cands); got != "gpt-4o-mini" {
		t.Fatalf("no cheaper configured model must not change, got %q", got)
	}
}
```

追加到 `internal/pricing/pricing_test.go`：

```go
func TestScore(t *testing.T) {
	s, ok := Score("deepseek-chat")
	if !ok || s < 1.36 || s > 1.38 {
		t.Fatalf("want deepseek-chat score ~1.37, got %v %v", s, ok)
	}
	if _, ok := Score("nope"); ok {
		t.Fatal("unknown model must report not-found")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/budget/ ./internal/pricing/ -v`
Expected: FAIL（`Model` 未定义、`Score` 未定义）

- [ ] **Step 3: 实现 budget.go**

删除 `cheaper` map，替换 `CheaperModel`：

```go
type Model struct {
	Name  string
	Price float64
}

func CheaperModel(current string, currentPrice float64, candidates []Model) string {
	if currentPrice <= 0 {
		return current
	}
	bestName, bestPrice := "", 0.0
	for _, m := range candidates {
		if m.Price <= 0 || m.Name == current {
			continue
		}
		if bestName == "" || m.Price < bestPrice {
			bestName, bestPrice = m.Name, m.Price
		}
	}
	if bestName != "" && bestPrice < currentPrice {
		return bestName
	}
	return current
}
```

- [ ] **Step 4: 实现 pricing.Score**

```go
func Score(model string) (float64, bool) {
	r, ok := table[model]
	if !ok {
		return 0, false
	}
	return r.in + r.out, true
}
```

- [ ] **Step 5: 运行 budget/pricing 测试**

Run: `go test ./internal/budget/ ./internal/pricing/ -v`
Expected: PASS

- [ ] **Step 6: router 构造候选并调用新签名**

在 `providers`/`models` 构建循环之后加入：
```go
	modelCandidates := make([]budget.Model, 0, len(s.Providers))
	for _, pc := range s.Providers {
		if score, ok := pricing.Score(pc.Model); ok {
			modelCandidates = append(modelCandidates, budget.Model{Name: pc.Model, Price: score})
		}
	}
```

非流式与流式两处降级块均改为：
```go
			case budget.Degrade:
				score, _ := pricing.Score(req.Model)
				req.Model = budget.CheaperModel(req.Model, score, modelCandidates)
```

- [ ] **Step 7: 更新降级测试为双 provider**

`internal/api/budget_test.go` 的 `TestChatBudgetDegradeSwapsModel`：把 Settings.Providers 改为两个（都指向 `upstream.URL`）：
```go
		Providers: []config.ProviderConfig{
			{Name: "deepseek", BaseURL: upstream.URL, APIKey: "k", Model: "deepseek-chat"},
			{Name: "openai", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"},
		},
```
断言保持：`gotModel == "deepseek-chat"`。

`internal/api/stream_test.go` 的 `TestChatStreamBudgetDegradeSwapsModel` 同样把 Providers 改为上述两个，断言保持 `gotModel == "deepseek-chat"`。

- [ ] **Step 8: 运行全量测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
gofmt -w internal/budget/ internal/pricing/ internal/api/
git add internal/budget/ internal/pricing/ internal/api/
git commit -m "fix: 预算降级改为选择已配置的更便宜模型"
```

---

### Task 5: 故障转移 Target + 重试退避 + 模型感知路由

**Files:**
- Modify: `internal/proxy/failover.go`
- Modify: `internal/proxy/failover_test.go`
- Modify: `internal/api/router.go`（`targetsForModel` + 调用点）

**Interfaces:**
- Consumes: `provider.IsRetryable(err)`（Task 1）。
- Produces:
  - `type proxy.Target struct { Provider provider.Provider; Model string }`
  - `var proxy.MaxAttempts int`（默认 3）
  - `var proxy.RetryBackoff time.Duration`（默认 200ms）
  - `func proxy.ChatWithFailover(ctx, targets []Target, req provider.ChatRequest) (provider.ChatResponse, provider.Provider, error)`
  - `func proxy.StreamWithFailover(ctx, targets []Target, req provider.ChatRequest) (<-chan provider.StreamChunk, provider.Provider, error)`

- [ ] **Step 1: 重写 failover.go**

```go
package proxy

import (
	"context"
	"errors"
	"time"

	"llm-gateway/internal/provider"
)

var (
	MaxAttempts  = 3
	RetryBackoff = 200 * time.Millisecond
)

type Target struct {
	Provider provider.Provider
	Model    string
}

func ChatWithFailover(ctx context.Context, targets []Target, req provider.ChatRequest) (provider.ChatResponse, provider.Provider, error) {
	if len(targets) == 0 {
		return provider.ChatResponse{}, nil, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return provider.ChatResponse{}, nil, ctx.Err()
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		resp, err := doWithRetry(ctx, func() (provider.ChatResponse, error) {
			return tgt.Provider.Chat(ctx, attemptReq)
		})
		if err == nil {
			return resp, tgt.Provider, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return provider.ChatResponse{}, nil, err
		}
	}
	return provider.ChatResponse{}, nil, lastErr
}

func StreamWithFailover(ctx context.Context, targets []Target, req provider.ChatRequest) (<-chan provider.StreamChunk, provider.Provider, error) {
	if len(targets) == 0 {
		return nil, nil, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		ch, err := doWithRetry(ctx, func() (<-chan provider.StreamChunk, error) {
			return tgt.Provider.ChatStream(ctx, attemptReq)
		})
		if err == nil {
			return ch, tgt.Provider, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return nil, nil, err
		}
	}
	return nil, nil, lastErr
}

func doWithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < MaxAttempts; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) || attempt == MaxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(RetryBackoff << attempt):
		}
	}
	return zero, lastErr
}
```

- [ ] **Step 2: 重写 failover_test.go**

```go
package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"llm-gateway/internal/provider"
)

type fakeProvider struct {
	name    string
	fail    bool
	content string
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if f.fail {
		return provider.ChatResponse{}, errors.New("boom")
	}
	return provider.ChatResponse{Content: f.content, TotalTokens: 1}, nil
}
func (f fakeProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	if f.fail {
		return nil, errors.New("boom")
	}
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: f.content}
	close(ch)
	return ch, nil
}

type scriptedProvider struct {
	name    string
	content string
	errs    []error
	calls   int
}

func (p *scriptedProvider) Name() string { return p.name }
func (p *scriptedProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	i := p.calls
	p.calls++
	if i < len(p.errs) && p.errs[i] != nil {
		return provider.ChatResponse{}, p.errs[i]
	}
	return provider.ChatResponse{Content: p.content}, nil
}
func (p *scriptedProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	i := p.calls
	p.calls++
	if i < len(p.errs) && p.errs[i] != nil {
		return nil, p.errs[i]
	}
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: p.content}
	close(ch)
	return ch, nil
}

func fastRetry(t *testing.T) {
	t.Helper()
	oldMax, oldBackoff := MaxAttempts, RetryBackoff
	MaxAttempts, RetryBackoff = 3, time.Millisecond
	t.Cleanup(func() { MaxAttempts, RetryBackoff = oldMax, oldBackoff })
}

func TestChatWithFailoverUsesBackup(t *testing.T) {
	fastRetry(t)
	targets := []Target{
		{Provider: fakeProvider{name: "primary", fail: true}, Model: "m"},
		{Provider: fakeProvider{name: "backup", content: "ok"}, Model: "b"},
	}
	resp, used, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if resp.Content != "ok" || used.Name() != "backup" {
		t.Fatalf("want backup/ok, got %s/%s", used.Name(), resp.Content)
	}
}

func TestChatWithFailoverRetriesRetryableThenSucceeds(t *testing.T) {
	fastRetry(t)
	p := &scriptedProvider{name: "flaky", content: "ok", errs: []error{
		provider.NewStatusError("flaky", 503, "boom"),
		nil,
	}}
	targets := []Target{{Provider: p, Model: "m"}}
	resp, _, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
	if resp.Content != "ok" || p.calls != 2 {
		t.Fatalf("want 2 calls and 'ok', got calls=%d content=%q", p.calls, resp.Content)
	}
}

func TestChatWithFailoverNonRetryableStopsImmediately(t *testing.T) {
	fastRetry(t)
	primary := &scriptedProvider{name: "primary", errs: []error{provider.NewStatusError("primary", 400, "bad")}}
	backup := &scriptedProvider{name: "backup", content: "ok"}
	targets := []Target{{Provider: primary, Model: "m"}, {Provider: backup, Model: "b"}}
	_, _, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err == nil {
		t.Fatal("want error for non-retryable failure")
	}
	if primary.calls != 1 {
		t.Fatalf("non-retryable must not retry, got %d calls", primary.calls)
	}
	if backup.calls != 0 {
		t.Fatalf("non-retryable must not fail over, backup calls=%d", backup.calls)
	}
}

func TestFailoverEmptyProvidersReturnsError(t *testing.T) {
	resp, used, err := ChatWithFailover(context.Background(), nil, provider.ChatRequest{})
	if err == nil {
		t.Fatalf("ChatWithFailover: want error, got nil (resp=%+v)", resp)
	}
	if used != nil {
		t.Fatalf("ChatWithFailover: want nil provider, got %v", used)
	}

	ch, usedStream, err := StreamWithFailover(context.Background(), nil, provider.ChatRequest{})
	if err == nil {
		t.Fatalf("StreamWithFailover: want error, got nil (ch=%v)", ch)
	}
	if usedStream != nil {
		t.Fatalf("StreamWithFailover: want nil provider, got %v", usedStream)
	}
}
```

- [ ] **Step 3: 运行 proxy 测试**

Run: `go test ./internal/proxy/ -v`
Expected: PASS

- [ ] **Step 4: router 增加 targetsForModel 并改用 Target**

在 `providers` 构建完成、`registerChat` 之前加入：

```go
	targetsForModel := func(model string) []proxy.Target {
		var owners, others []proxy.Target
		for i, pc := range s.Providers {
			if pc.Model == model {
				owners = append(owners, proxy.Target{Provider: providers[i], Model: model})
			} else {
				others = append(others, proxy.Target{Provider: providers[i], Model: pc.Model})
			}
		}
		if len(owners) > 0 {
			return append(owners, others...)
		}
		if len(providers) == 0 {
			return nil
		}
		return append([]proxy.Target{{Provider: providers[0], Model: model}}, others...)
	}
```

非流式调用点：
```go
			resp, used, err := proxy.ChatWithFailover(c.Request.Context(), targetsForModel(req.Model), req)
```
流式调用点：
```go
			ch, used, err := proxy.StreamWithFailover(c.Request.Context(), targetsForModel(req.Model), req)
```
（`len(providers) == 0` 的既有守卫保持不变，位于调用之前。）

- [ ] **Step 5: 新增路由回退测试**

创建 `internal/api/routing_test.go`：

```go
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
)

func TestChatFailsOverToBackupWithItsOwnModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldMax, oldBackoff := proxy.MaxAttempts, proxy.RetryBackoff
	proxy.MaxAttempts, proxy.RetryBackoff = 1, time.Millisecond
	defer func() { proxy.MaxAttempts, proxy.RetryBackoff = oldMax, oldBackoff }()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer primary.Close()

	var backupModel string
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		backupModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"model":"backup-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer backup.Close()

	s := config.Settings{
		GatewayToken: "tok", DBPath: "file:routing?mode=memory&cache=shared", BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{
			{Name: "primary", BaseURL: primary.URL, APIKey: "k", Model: "primary-model"},
			{Name: "backup", BaseURL: backup.URL, APIKey: "k", Model: "backup-model"},
		},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"primary-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	if backupModel != "backup-model" {
		t.Fatalf("backup provider must receive its own model, got %q", backupModel)
	}
}
```

- [ ] **Step 6: 运行全量测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
gofmt -w internal/proxy/ internal/api/
git add internal/proxy/ internal/api/
git commit -m "fix: 故障转移按错误分类重试退避, 备用 provider 使用自身模型"
```

---

### Task 6: 缓存命中落库（记命中 + 节省金额）+ 看板

**Files:**
- Modify: `internal/api/router.go`
- Modify: `internal/api/web/index.html`
- Create: `internal/api/cache_accounting_test.go`

**Interfaces:**
- Consumes: `usage.Record.SavedCost`、`repo.Record`（Task 3）；`cache.CachedResult`（Task 2）。
- Produces: 命中时落库一条 `Provider="cache"`、`CacheHit=true`、`Cost=0`、`SavedCost=pricing.Cost(...)` 的记录。

- [ ] **Step 1: 写失败测试**

创建 `internal/api/cache_accounting_test.go`：

```go
package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

func TestChatCacheHitIsRecordedWithSavedCost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"deepseek-chat","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":1000,"total_tokens":2000}}`)
	}))
	defer upstream.Close()

	db := "file:cache_accounting?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "deepseek-chat"}},
	}
	r := NewRouter(s)

	send := func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
		}
	}
	send()
	send()

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()

	recs, err := repo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var hit *usage.Record
	for i := range recs {
		if recs[i].CacheHit {
			hit = &recs[i]
		}
	}
	if hit == nil {
		t.Fatalf("want a cache_hit record, got %+v", recs)
	}
	if hit.SavedCost <= 0 {
		t.Fatalf("want saved_cost > 0, got %v", hit.SavedCost)
	}
	if hit.Cost != 0 {
		t.Fatalf("cache hit must not add to cost, got %v", hit.Cost)
	}
	if saved, _ := repo.MonthlySaved(context.Background(), usage.CurrentMonth()); saved <= 0 {
		t.Fatalf("want month saved > 0, got %v", saved)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/api/ -run TestChatCacheHitIsRecordedWithSavedCost -v`
Expected: FAIL（`want a cache_hit record`）

- [ ] **Step 3: 在 router 增加命中计量**

在 `recordUsage` 定义之后新增：

```go
	recordCacheHit := func(model string, promptTokens, completionTokens int) {
		_ = repo.Record(context.Background(), usage.Record{
			ID:               newRequestID(),
			Model:            model,
			Provider:         "cache",
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			Cost:             0,
			SavedCost:        pricing.Cost(model, promptTokens, completionTokens),
			CacheHit:         true,
		})
	}
```

非流式精确命中：在 `m.Requests...Inc()` 之后、`return` 之前调用：
```go
				recordCacheHit(req.Model, cached.PromptTokens, cached.CompletionTokens)
```
非流式语义命中：
```go
				recordCacheHit(req.Model, hit.PromptTokens, hit.CompletionTokens)
```
流式精确命中：
```go
				recordCacheHit(req.Model, cached.PromptTokens, cached.CompletionTokens)
```

- [ ] **Step 4: 运行 api 测试**

Run: `go test ./internal/api/ -run TestChatCacheHitIsRecordedWithSavedCost -v`
Expected: PASS

- [ ] **Step 5: 看板显示本月节省**

在 `internal/api/web/index.html` 中，把
```html
<p>本月花费：<span id="cost">-</span> 元</p>
```
改为
```html
<p>本月花费：<span id="cost">-</span> 元 ｜ 本月节省：<span id="saved">-</span> 元</p>
```
并在 `document.getElementById('cost').textContent = ...` 之后加：
```js
  document.getElementById('saved').textContent = (data.month_saved || 0).toFixed(3);
```

- [ ] **Step 6: 运行全量测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
gofmt -w internal/api/
git add internal/api/
git commit -m "feat: 缓存命中落库节省金额, 看板显示本月节省"
```

---

### Task 7: 文档更新与端到端验收

**Files:**
- Modify: `README.md`
- Modify: `examples/README.md`（如无需改动可跳过）

**Interfaces:** 无。

- [ ] **Step 1: 更新 README 行为描述**

在 README 的"功能特性"与"技术栈与取舍"之间/相应位置，补充以下事实（保持中文、与现状一致）：

- "故障转移"条目改为：主 provider 遇到**可重试错误**（网络/超时/5xx/429）时先在同一 provider 做指数退避重试（默认 3 次），仍失败才切换到备用 provider；备用 provider 使用**其自身配置的模型**；不可重试错误（其它 4xx）直接返回，不切换。
- "降本缓存"条目补充：命中会写入用量记录（`cache_hit=true`）并按原模型价格计算 `saved_cost`；`/v1/admin/usage` 返回 `month_saved`。
- "预算护栏"条目补充：降级只会在存在"更便宜的、已配置的模型"时发生，否则保持原模型。

`/v1/admin/usage` 示例 JSON 增加字段：
```json
{
  "month_cost": 0.0123,
  "month_saved": 0.0004,
  "records": [
    {"id": "...", "model": "deepseek-chat", "provider": "cache",
     "prompt_tokens": 20, "completion_tokens": 40, "cost": 0, "saved_cost": 0.00003,
     "cache_hit": true, "created_at": "..."}
  ]
}
```

- [ ] **Step 2: 静态检查与全量测试**

Run: `gofmt -l .; go vet ./...; go test ./...`
Expected: `gofmt -l` 无输出；`go vet` 无告警；全部测试 PASS

- [ ] **Step 3: 端到端人工复现（无需真实 key）**

按以下步骤验证（临时目录，不改仓库）：

1. 在临时目录准备 mock 上游（`/v1/chat/completions` 非流式返 JSON、流式返 SSE + usage + `[DONE]`）。
2. 启动网关（`GATEWAY_TOKEN=my-secret-123`、`DEEPSEEK_API_KEY=fake`、`DEEPSEEK_BASE_URL=http://127.0.0.1:9999/v1`、`DB_PATH=<临时>gateway.db`）。
3. `curl` 同一非流式请求两次。
4. `curl /v1/admin/usage -H "Authorization: Bearer my-secret-123"`。
Expected: 出现 `cache_hit=true` 且 `saved_cost>0` 的记录，`month_saved>0`。
5. 关闭进程，清理临时文件。

- [ ] **Step 4: 提交并推送**

```bash
git add README.md
git commit -m "docs: 更新故障转移/缓存计量/降级行为说明"
git push -u origin feat/llm-gateway-impl
```

---

## Self-Review

**Spec coverage:**
- 缓存命中落库 + saved_cost + month_saved + 看板 → Task 2（token 元数据）、Task 3（存储/迁移/接口）、Task 6（编排/看板）。
- 降级基于已配置模型 → Task 4。
- 故障转移错误分类 + 重试退避 + 模型感知路由 → Task 1（分类）、Task 5（重试/路由）。
- 验收与文档 → Task 7。
- 覆盖完整，无遗漏。

**Placeholder scan:** 无 TBD/TODO；每个代码步骤含完整代码。

**Type consistency:**
- `CachedResult{Content, PromptTokens, CompletionTokens}` 在 Task 2 定义，Task 2/Task 6 一致使用。
- `budget.Model{Name, Price}` 与 `CheaperModel(current, currentPrice, candidates)` 在 Task 4 定义并使用。
- `proxy.Target{Provider, Model}` 与 `ChatWithFailover(ctx, targets, req)` 在 Task 5 定义并使用；`MaxAttempts`/`RetryBackoff` 命名一致。
- `Record.SavedCost`/`MonthlySaved` 在 Task 3 定义，Task 6 使用。
- `provider.NewStatusError`/`IsRetryable` 在 Task 1 定义，Task 5 测试使用。