# 价格表分时计价与远程热更新 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 让价格表支持高峰/空闲分时计价，并可从远程 JSON 定时热更新，无需重编译；加入 `deepseek-v4.1-flash`。

**Architecture:** 把 `internal/pricing` 从静态 map 改成"内置默认表（go:embed）+ 可被远程覆盖的内存 Store"；`Cost`/`Score` 签名不变，内部按 `NowFunc()` 在中国时区判定分时价。远程拉取由 `StartAutoRefresh` 在 `main` 启动。新增只读 `GET /v1/admin/pricing`。

**Tech Stack:** Go 1.26、net/http、encoding/json、time/tzdata。

## Global Constraints

- Go 1.26.7；不新增任何第三方依赖。
- 保持包分层；不新增顶层包。
- 所有改动通过 `gofmt`；`go vet ./...`、`go test ./...` 全绿。
- 注释只写"为什么"，不写"做了什么"。
- 提交风格：`feat:` / `improve:` / `docs:`。
- 价格单位：元 / 百万 token；时区 `Asia/Shanghai`。
- `Cost(model string, promptTokens, completionTokens int) float64` 与 `Score(model string) (float64, bool)` 的签名**保持不变**（调用点不动）。

---

### Task 1: pricing 核心（分时价 + 内置默认表）

**Files:**
- Create: `internal/pricing/default.json`
- Modify: `internal/pricing/pricing.go`（整体重写）
- Modify: `internal/pricing/pricing_test.go`

**Interfaces:**
- Produces（导出类型，供 Task 2/4 使用）：
  - `type Rate struct { In, Out float64 }`
  - `type OffPeak struct { In, Out float64; Start, End string }`
  - `type ModelPrice struct { In, Out float64; OffPeak *OffPeak }`
  - `type Doc struct { Models map[string]ModelPrice }`
  - `type Store struct { mu sync.RWMutex; models map[string]ModelPrice; lastRefresh time.Time; source, lastErr string }`
  - `var NowFunc = time.Now`
  - `func Cost(model string, promptTokens, completionTokens int) float64`
  - `func Score(model string) (float64, bool)`
  - （包内部）`func loadDefault() map[string]ModelPrice`、`func effectiveRate(mp ModelPrice, now time.Time) Rate`、`func inWindow(now time.Time, start, end string) bool`、`func parseClock(hhmm string) (int, bool)`、`var store *Store`

- [ ] **Step 1: 写失败测试**

替换 `internal/pricing/pricing_test.go` 为：

```go
package pricing

import (
	"testing"
	"time"
)

func withNow(t *testing.T, at time.Time) {
	t.Helper()
	old := NowFunc
	NowFunc = func() time.Time { return at }
	t.Cleanup(func() { NowFunc = old })
}

func TestCostKnownModel(t *testing.T) {
	withNow(t, time.Date(2026, 1, 1, 12, 0, 0, 0, location))
	got := Cost("deepseek-chat", 1_000_000, 1_000_000)
	want := 1.37
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestCostUnknownModelFallsBack(t *testing.T) {
	if Cost("unknown-model", 1000, 1000) != 0 {
		t.Fatal("unknown model should cost 0")
	}
}

func TestDeepSeekV41FlashPeakAndOffPeak(t *testing.T) {
	withNow(t, time.Date(2026, 1, 1, 12, 0, 0, 0, location)) // 12:00 高峰
	got := Cost("deepseek-v4.1-flash", 1_000_000, 1_000_000)
	if diff := got - 10.0; diff > 1e-9 || diff < -1e-9 { // 2 + 8
		t.Fatalf("peak want 10, got %v", got)
	}
	withNow(t, time.Date(2026, 1, 1, 3, 0, 0, 0, location)) // 03:00 空闲
	got = Cost("deepseek-v4.1-flash", 1_000_000, 1_000_000)
	if diff := got - 5.0; diff > 1e-9 || diff < -1e-9 { // 1 + 4
		t.Fatalf("off-peak want 5, got %v", got)
	}
}

func TestOffPeakBoundaries(t *testing.T) {
	cases := []struct {
		hh, mm int
		want   float64 // 每百万输入价：高峰 2，空闲 1
	}{
		{0, 29, 2}, {0, 30, 1}, {8, 29, 1}, {8, 30, 2}, {23, 59, 2},
	}
	for _, tc := range cases {
		withNow(t, time.Date(2026, 1, 1, tc.hh, tc.mm, 0, 0, location))
		got := Cost("deepseek-v4.1-flash", 1_000_000, 0)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("%02d:%02d want %v got %v", tc.hh, tc.mm, tc.want, got)
		}
	}
}

func TestInWindowCrossMidnight(t *testing.T) {
	mp := ModelPrice{In: 2, Out: 8, OffPeak: &OffPeak{In: 1, Out: 4, Start: "22:00", End: "06:00"}}
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, location) }
	if effectiveRate(mp, at(23)).In != 1 || effectiveRate(mp, at(2)).In != 1 {
		t.Fatal("23:00 and 02:00 should be off-peak for 22:00-06:00")
	}
	if effectiveRate(mp, at(12)).In != 2 || effectiveRate(mp, at(6)).In != 2 {
		t.Fatal("12:00 and 06:00 should be peak for 22:00-06:00")
	}
}

func TestScoreUsesPeakRate(t *testing.T) {
	s, ok := Score("deepseek-v4.1-flash")
	if !ok || s != 10 {
		t.Fatalf("want score 10 (peak 2+8), got %v %v", s, ok)
	}
	if _, ok := Score("nope"); ok {
		t.Fatal("unknown model must report not-found")
	}
}

func TestDefaultTableHasModels(t *testing.T) {
	for _, m := range []string{"deepseek-chat", "gpt-4o-mini", "qwen-turbo", "qwen-plus", "qwen-max", "qwen3.8-flash", "deepseek-v4.1-flash"} {
		if _, ok := Score(m); !ok {
			t.Fatalf("embedded default table missing %q", m)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/pricing/ -v`
Expected: FAIL（`location`/`ModelPrice`/`OffPeak`/`effectiveRate` 未定义）

- [ ] **Step 3: 创建 default.json**

`internal/pricing/default.json`：

```json
{
  "models": {
    "deepseek-chat": { "in": 0.27, "out": 1.10 },
    "gpt-4o-mini": { "in": 1.10, "out": 4.40 },
    "qwen-turbo": { "in": 0.30, "out": 0.60 },
    "qwen-plus": { "in": 0.80, "out": 2.00 },
    "qwen-max": { "in": 2.40, "out": 9.60 },
    "qwen3.8-flash": { "in": 0.80, "out": 2.70 },
    "deepseek-v4.1-flash": {
      "in": 2.0,
      "out": 8.0,
      "off_peak": { "in": 1.0, "out": 4.0, "start": "00:30", "end": "08:30" }
    }
  }
}
```

- [ ] **Step 4: 重写 pricing.go**

```go
package pricing

import (
	_ "embed"
	"encoding/json"
	"sync"
	"time"

	_ "time/tzdata"
)

//go:embed default.json
var defaultJSON []byte

// NowFunc is replaceable in tests.
var NowFunc = time.Now

var location = mustLoadLocation("Asia/Shanghai")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

type Rate struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

type OffPeak struct {
	In    float64 `json:"in"`
	Out   float64 `json:"out"`
	Start string  `json:"start"`
	End   string  `json:"end"`
}

type ModelPrice struct {
	In      float64  `json:"in"`
	Out     float64  `json:"out"`
	OffPeak *OffPeak `json:"off_peak,omitempty"`
}

type Doc struct {
	Models map[string]ModelPrice `json:"models"`
}

type Store struct {
	mu          sync.RWMutex
	models      map[string]ModelPrice
	lastRefresh time.Time
	source      string
	lastErr     string
}

var store = func() *Store {
	return &Store{models: loadDefault(), source: "embedded"}
}()

func loadDefault() map[string]ModelPrice {
	var doc Doc
	if err := json.Unmarshal(defaultJSON, &doc); err != nil {
		panic("pricing: invalid embedded default.json: " + err.Error())
	}
	return doc.Models
}

func Cost(model string, promptTokens, completionTokens int) float64 {
	return costAt(model, promptTokens, completionTokens, NowFunc())
}

func costAt(model string, promptTokens, completionTokens int, now time.Time) float64 {
	store.mu.RLock()
	mp, ok := store.models[model]
	store.mu.RUnlock()
	if !ok {
		return 0
	}
	r := effectiveRate(mp, now)
	return float64(promptTokens)/1e6*r.In + float64(completionTokens)/1e6*r.Out
}

func effectiveRate(mp ModelPrice, now time.Time) Rate {
	if mp.OffPeak != nil && inWindow(now.In(location), mp.OffPeak.Start, mp.OffPeak.End) {
		return Rate{In: mp.OffPeak.In, Out: mp.OffPeak.Out}
	}
	return Rate{In: mp.In, Out: mp.Out}
}

func inWindow(now time.Time, start, end string) bool {
	s, ok := parseClock(start)
	if !ok {
		s = 30 // 00:30 默认空闲起点
	}
	e, ok := parseClock(end)
	if !ok {
		e = 8*60 + 30 // 08:30 默认空闲终点
	}
	if s == e {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if s < e {
		return m >= s && m < e
	}
	return m >= s || m < e
}

func parseClock(hhmm string) (int, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func Score(model string) (float64, bool) {
	store.mu.RLock()
	mp, ok := store.models[model]
	store.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return mp.In + mp.Out, true
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/pricing/ -v`
Expected: PASS

- [ ] **Step 6: 全量测试 + 提交**

Run: `go test ./...`
Expected: PASS

```bash
gofmt -w internal/pricing/
git add internal/pricing/
git commit -m "feat: 价格表支持高峰/空闲分时价, 内置默认表新增 deepseek-v4.1-flash"
```

---

### Task 2: pricing 远程热更新

**Files:**
- Create: `internal/pricing/refresh.go`
- Create: `internal/pricing/refresh_test.go`

**Interfaces:**
- Consumes: Task 1 的 `store`、`loadDefault()`、`parseClock`、`NowFunc`、`Doc`、`ModelPrice`、`Store`。
- Produces:
  - `func Refresh(ctx context.Context, rawURL string) error`
  - `func StartAutoRefresh(rawURL string, interval time.Duration)`
  - `type Snapshot struct { Source string; LastRefresh time.Time; LastError string; Models map[string]ModelPrice }`
  - `func Snapshot() Snapshot`（注意：与类型同名，改为 `func Snapshot() Snapshot` 不可行——因此函数名用 `func Take() Snapshot`）

  最终命名：`func Take() Snapshot`（避免函数与类型同名）。

- [ ] **Step 1: 写失败测试**

`internal/pricing/refresh_test.go`：

```go
package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshMergesRemoteOverDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{
			"deepseek-chat":{"in":9,"out":9},
			"new-model":{"in":3,"out":4}
		}}`))
	}))
	defer srv.Close()

	if err := Refresh(context.Background(), srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if c := costAt("deepseek-chat", 1_000_000, 0, NowFunc()); c != 9 {
		t.Fatalf("remote should override deepseek-chat in=9, got %v", c)
	}
	if c := costAt("new-model", 1_000_000, 0, NowFunc()); c != 3 {
		t.Fatalf("remote should add new-model in=3, got %v", c)
	}
	if _, ok := Score("qwen-plus"); !ok {
		t.Fatal("models not in remote must keep embedded default")
	}
	snap := Take()
	if snap.Source != srv.URL || snap.LastError != "" || snap.LastRefresh.IsZero() {
		t.Fatalf("snapshot after refresh unexpected: %+v", snap)
	}
}

func TestRefreshRejectsNonHTTPScheme(t *testing.T) {
	if err := Refresh(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
}

func TestRefreshKeepsOldTableOnError(t *testing.T) {
	before := costAt("deepseek-chat", 1_000_000, 0, NowFunc())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("non-2xx must error")
	}

	if after := costAt("deepseek-chat", 1_000_000, 0, NowFunc()); after != before {
		t.Fatalf("failed refresh must not change table: before %v after %v", before, after)
	}
	if Take().LastError == "" {
		t.Fatal("failed refresh should record last_error")
	}
}

func TestRefreshRejectsBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("bad json must error")
	}
}

func TestRefreshRejectsBadWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"m":{"in":1,"out":1,"off_peak":{"in":1,"out":1,"start":"99:99","end":"08:30"}}}}`))
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("bad off_peak window must error")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/pricing/ -run TestRefresh -v`
Expected: FAIL（`Refresh`/`Take`/`Snapshot` 未定义）

- [ ] **Step 3: 实现 refresh.go**

```go
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const maxPricingBytes = 1 << 20 // 1MB

var httpClient = &http.Client{Timeout: 10 * time.Second}

type Snapshot struct {
	Source      string                `json:"source"`
	LastRefresh time.Time             `json:"last_refresh"`
	LastError   string                `json:"last_error,omitempty"`
	Models      map[string]ModelPrice `json:"models"`
}

func Refresh(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("pricing: unsupported url scheme %q", u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		err := fmt.Errorf("pricing: refresh status %d", res.StatusCode)
		store.setErr(err.Error())
		return err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPricingBytes))
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	var doc Doc
	if err := json.Unmarshal(body, &doc); err != nil {
		store.setErr(err.Error())
		return err
	}
	if err := validateDoc(doc); err != nil {
		store.setErr(err.Error())
		return err
	}
	merged := loadDefault()
	for name, mp := range doc.Models {
		merged[name] = mp
	}
	store.replace(merged, rawURL)
	return nil
}

func validateDoc(doc Doc) error {
	for name, mp := range doc.Models {
		if mp.In < 0 || mp.Out < 0 {
			return fmt.Errorf("pricing: model %s has negative rate", name)
		}
		if mp.OffPeak != nil {
			if mp.OffPeak.In < 0 || mp.OffPeak.Out < 0 {
				return fmt.Errorf("pricing: model %s off_peak negative rate", name)
			}
			if _, ok := parseClock(mp.OffPeak.Start); !ok {
				return fmt.Errorf("pricing: model %s bad off_peak.start %q", name, mp.OffPeak.Start)
			}
			if _, ok := parseClock(mp.OffPeak.End); !ok {
				return fmt.Errorf("pricing: model %s bad off_peak.end %q", name, mp.OffPeak.End)
			}
		}
	}
	return nil
}

func (s *Store) setErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *Store) replace(models map[string]ModelPrice, source string) {
	s.mu.Lock()
	s.models = models
	s.source = source
	s.lastRefresh = NowFunc()
	s.lastErr = ""
	s.mu.Unlock()
}

func Take() Snapshot {
	store.mu.RLock()
	defer store.mu.RUnlock()
	models := make(map[string]ModelPrice, len(store.models))
	for k, v := range store.models {
		models[k] = v
	}
	return Snapshot{Source: store.source, LastRefresh: store.lastRefresh, LastError: store.lastErr, Models: models}
}

func StartAutoRefresh(rawURL string, interval time.Duration) {
	if rawURL == "" {
		return
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		ctx := context.Background()
		if err := Refresh(ctx, rawURL); err != nil {
			log.Printf("pricing: initial refresh failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := Refresh(ctx, rawURL); err != nil {
				log.Printf("pricing: refresh failed: %v", err)
			}
		}
	}()
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/pricing/ -v`
Expected: PASS

- [ ] **Step 5: 全量测试 + 提交**

Run: `go test ./...`
Expected: PASS

```bash
gofmt -w internal/pricing/
git add internal/pricing/
git commit -m "feat: 价格表支持远程 JSON 定时热更新"
```

---

### Task 3: config 增加定价刷新配置

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `Settings.PricingURL string`（← `PRICING_URL`）
  - `Settings.PricingRefreshInterval time.Duration`（← `PRICING_REFRESH_INTERVAL`，默认 6h）

- [ ] **Step 1: 写失败测试**

追加到 `internal/config/config_test.go`：

```go
func TestLoadPricingSettings(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "PRICING_URL=https://example.com/pricing.json\nPRICING_REFRESH_INTERVAL=30m\n")
	clearEnv(t, "PRICING_URL", "PRICING_REFRESH_INTERVAL")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.PricingURL != "https://example.com/pricing.json" {
		t.Fatalf("want pricing url from .env, got %q", s.PricingURL)
	}
	if s.PricingRefreshInterval != 30*time.Minute {
		t.Fatalf("want 30m interval, got %v", s.PricingRefreshInterval)
	}
}

func TestLoadPricingDefaultsTo6h(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "GATEWAY_TOKEN=x\n")
	clearEnv(t, "PRICING_URL", "PRICING_REFRESH_INTERVAL", "GATEWAY_TOKEN")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.PricingURL != "" {
		t.Fatalf("want empty pricing url, got %q", s.PricingURL)
	}
	if s.PricingRefreshInterval != 6*time.Hour {
		t.Fatalf("want 6h default, got %v", s.PricingRefreshInterval)
	}
}
```

（在文件顶部 import 增加 `"time"`。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config/ -run TestLoadPricing -v`
Expected: FAIL（`PricingURL`/`PricingRefreshInterval` 未定义）

- [ ] **Step 3: 实现 config.go**

在 `Settings` 增加字段：

```go
type Settings struct {
	GatewayToken           string
	ListenAddr             string
	DBPath                 string
	BudgetMonthlyLimit     float64
	BudgetSoftRatio        float64
	PricingURL             string
	PricingRefreshInterval time.Duration
	Providers              []ProviderConfig
}
```

在 `Load()` 的 `s := Settings{...}` 增加：

```go
		PricingURL:             os.Getenv("PRICING_URL"),
		PricingRefreshInterval: getduration("PRICING_REFRESH_INTERVAL", 6*time.Hour),
```

新增 helper 与 import（`"time"`）：

```go
func getduration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofmt -w internal/config/
git add internal/config/
git commit -m "feat: 新增 PRICING_URL / PRICING_REFRESH_INTERVAL 配置"
```

---

### Task 4: 装配 + /v1/admin/pricing + 文档

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/api/router.go`
- Create: `internal/api/pricing.go`
- Create: `internal/api/pricing_test.go`
- Modify: `configs/.env.example`
- Modify: `README.md`

**Interfaces:**
- Consumes: `pricing.StartAutoRefresh`、`pricing.Take()`、`config.Settings.PricingURL/PricingRefreshInterval`。

- [ ] **Step 1: 写失败测试**

`internal/api/pricing_test.go`：

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func TestAdminPricingReturnsTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := config.Settings{GatewayToken: "tok", DBPath: "file:admin_pricing?mode=memory&cache=shared"}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/pricing", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	models, ok := payload["models"].(map[string]any)
	if !ok {
		t.Fatalf("missing models, body=%q", w.Body.String())
	}
	if _, ok := models["deepseek-v4.1-flash"]; !ok {
		t.Fatalf("expected deepseek-v4.1-flash in pricing, got %v", models)
	}
	if payload["source"] == "" {
		t.Fatal("expected a source field")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/api/ -run TestAdminPricing -v`
Expected: FAIL（404 / 无 models）

- [ ] **Step 3: 新增 handler 并注册**

`internal/api/pricing.go`：

```go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/pricing"
)

func registerPricing(v1 *gin.RouterGroup) {
	v1.GET("/admin/pricing", func(c *gin.Context) {
		c.JSON(http.StatusOK, pricing.Take())
	})
}
```

在 `internal/api/router.go` 的 `registerAdmin(v1, repo)` 之后加一行：

```go
	registerPricing(v1)
```

- [ ] **Step 4: main 启动自动刷新**

`cmd/server/main.go` 改为（新增 pricing import 与一行调用）：

```go
package main

import (
	"log"

	"llm-gateway/internal/api"
	"llm-gateway/internal/config"
	"llm-gateway/internal/pricing"
)

func main() {
	s, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	pricing.StartAutoRefresh(s.PricingURL, s.PricingRefreshInterval)
	r := api.NewRouter(s)
	log.Printf("listening on %s", s.ListenAddr)
	if err := r.Run(s.ListenAddr); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 5: 运行 api 测试**

Run: `go test ./internal/api/ -run TestAdminPricing -v`
Expected: PASS

- [ ] **Step 6: 更新 .env.example 与 README**

`configs/.env.example` 追加（放在 budget 配置之后）：

```dotenv
# 价格表远程刷新（可选）：指向一份 JSON 价格表；留空则只用内置默认价
PRICING_URL=
PRICING_REFRESH_INTERVAL=6h
```

README：在"功能特性"或"其他端点"里补充：
- 功能特性新增一条：**分时计价与价格热更新**——价格表支持高峰/空闲两档价（按请求时刻、北京时间判定），并可从 `PRICING_URL` 指向的 JSON 启动时 + 定时热更新，无需重编译；不配置则用内置默认价。
- `/v1/admin/usage` 表格下方补一行端点：`GET /v1/admin/pricing` —— 查看当前价格表（source / last_refresh / models）。

- [ ] **Step 7: 全量测试 + 提交**

Run: `gofmt -l .; go vet ./...; go test ./...`
Expected: 全部干净 / PASS

```bash
git add cmd/server/main.go internal/api/ configs/.env.example README.md
git commit -m "feat: 启动时自动刷新价格表, 新增 GET /v1/admin/pricing, 文档"
```

---

### Task 5: 端到端验收

**Files:** 无（仅验证）

- [ ] **Step 1: 静态检查与全量测试**

Run: `gofmt -l .; go vet ./...; go test ./...`
Expected: `gofmt -l` 无输出；`vet` 无告警；测试全绿。

- [ ] **Step 2: 本地端到端**

1. `go build -o <temp>/gateway.exe ./cmd/server`，从仓库目录以 `.env` 启动。
2. `curl /v1/admin/pricing`（带 token）应看到内置表含 `deepseek-v4.1-flash`，`source=business="embedded"`。
3. 在临时目录写一份 `pricing.json`（把 `deepseek-chat` 改成 `{"in":9,"out":9}`，新增一个模型），用 `python -m http.server` 或任意静态服务暴露，设置 `PRICING_URL` 后重启网关，再 `curl /v1/admin/pricing`，`source` 变为该 URL、`deepseek-chat` 变为 9/9。
4. 说明：分时价在 12:00 用高峰、03:00 用空闲（`Cost` 单测已覆盖）。

- [ ] **Step 3: 推送**

```bash
git push origin feat/llm-gateway-impl
```

---

## Self-Review

**Spec coverage：**
- 分时价 + 内置默认表 + deepseek-v4.1-flash → Task 1。
- 远程 JSON 定时热更新 + 校验 + 失败保留旧表 → Task 2。
- 配置 → Task 3。
- 装配 + admin 接口 + 文档 → Task 4。
- 验收 → Task 5。

**Placeholder scan：** 无 TBD/TODO；每个代码步骤含完整代码。

**Type consistency：**
- `ModelPrice`/`OffPeak`/`Rate`/`Doc` 在 Task 1 定义，Task 2/4 一致使用。
- `Take() Snapshot` 在 Task 2 定义，Task 4 使用（避免函数与类型同名的命名已明确）。
- `Cost`/`Score` 签名 Task 1 起不变，Task 2 测试用 `costAt`（内部）。