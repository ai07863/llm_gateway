# LLM Gateway 看板优化设计

- 日期：2026-10-29
- 状态：待评审

## 1. 背景

当前看板（`internal/api/web/index.html`）只展示：

- 本月花费 / 本月节省
- 单次成本折线图（最近 50 条）
- 最近 100 条调用明细表

网关其实已经持有更多可展示的数据：`/metrics`（请求数、状态、延迟直方图、token）、`/v1/admin/pricing`（单价与夜间折扣）、`usage_records`（缓存命中、节省成本）、以及 `.env` 中的预算配置。这些都没有进入看板。

同时发现一个现网问题：图表库 Chart.js 从 `https://cdn.jsdelivr.net` 加载，实测该 CDN 间歇性不可达（返回 000），导致图表经常画不出来。

### 目标

在同一页看板上呈现五类信息：

1. 成本分析：按模型 / 供应商分摊花费，缓存节省占比。
2. 预算监控：本月额度使用率、剩余、是否进入降级 / 拦截。
3. 运行健康：请求数、错误率、延迟 p50/p95、按状态分布。
4. 趋势图：按天与按小时两种粒度的成本 / token 曲线。
5. 调用明细增强：按模型 / 供应商 / 缓存筛选、搜索、服务端分页。

### 非目标

- 不做多用户 / 登录体系，沿用现有单一 gateway token。
- 不做历史数据的归档或导出。
- 不引入 Node/React 构建链。
- 不改动 `/v1` 代理主链路的行为与计费逻辑。

## 2. 方案

采用「后端聚合接口 + 原生单页 + 本地内置图表库」：

- 新增三个只读接口：`/v1/admin/summary`、`/v1/admin/requests`、`/v1/admin/health`。
- 重写 `web/index.html`，仍为单文件前端，零构建。
- Chart.js 下载后 `go:embed`，由网关自身提供，移除 CDN 依赖。

选择理由：数据由 SQL 聚合，准确且可扩展；部署仍是单一二进制；不需要前端构建工具链。

## 3. 数据与接口契约

### 3.1 `GET /v1/admin/summary`

查询参数：`month`（可选，`YYYY-MM`，默认当前月）。非法格式返回 400。

响应：

```json
{
  "month": "2026-10",
  "budget": {
    "limit": 0,
    "soft_ratio": 0.8,
    "cost": 20.034,
    "saved": 0.0023,
    "used_ratio": 0,
    "degraded": false,
    "rejected": false
  },
  "totals": {
    "requests": 123,
    "prompt_tokens": 1234567,
    "completion_tokens": 8901,
    "cost": 20.034,
    "saved": 0.0023,
    "cache_hits": 5,
    "cache_hit_ratio": 0.0407
  },
  "by_model": [
    {
      "model": "deepseek-v4.1-flash",
      "requests": 120,
      "prompt_tokens": 1200000,
      "completion_tokens": 8000,
      "cost": 19.9,
      "saved": 0.0
    }
  ],
  "by_provider": [
    {
      "provider": "dashscope",
      "requests": 123,
      "prompt_tokens": 1234567,
      "completion_tokens": 8901,
      "cost": 20.034,
      "saved": 0.0023
    }
  ],
  "by_day": [
    {
      "day": "2026-10-29",
      "requests": 40,
      "prompt_tokens": 500000,
      "completion_tokens": 3000,
      "cost": 8.2
    }
  ],
  "by_hour": [
    {
      "hour": "2026-10-29 14:00",
      "requests": 6,
      "prompt_tokens": 70000,
      "completion_tokens": 500,
      "cost": 1.1
    }
  ]
}
```

约定：

- `budget.limit <= 0` 表示预算拦截已关闭，此时 `used_ratio = 0`、`degraded/rejected = false`。
- `used_ratio = limit > 0 ? cost / limit : 0`。
- `degraded = limit > 0 && cost >= limit*soft_ratio && cost < limit`。
- `rejected = limit > 0 && cost >= limit`。
- `cache_hit_ratio = requests > 0 ? cache_hits / requests : 0`。
- `by_day` 覆盖所选月份的全部有数据日期；`by_hour` 固定覆盖最近 48 小时（含当前小时），与 `month` 参数无关。看板当前只请求当月，`month` 参数为后续扩展保留。
- 前端「今日 / 近7天 / 近30天 / 本月」只是对 `by_day` 做客户端切片；「按小时」视图使用 `by_hour`。
- 无数据时各数组为空数组（`[]`），不是 `null`。

### 3.2 `GET /v1/admin/requests`

查询参数（全部可选）：

- `model`：精确匹配。
- `provider`：精确匹配。
- `cache`：`hit` | `miss` | 空（全部）。
- `q`：对 `id`、`model` 做模糊包含匹配。
- `limit`：默认 50，范围 1–200。
- `offset`：默认 0，`>= 0`。

响应：

```json
{ "total": 137, "records": [ /* usage.Record */ ] }
```

`total` 为应用过滤条件后的总数；`records` 按 `created_at DESC` 排序。非法参数返回 400。空结果返回 `{"total":0,"records":[]}`。

### 3.3 `GET /v1/admin/health`

从 Prometheus registry 汇总（进程内指标，重启清零）：

```json
{
  "requests_total": 100,
  "error_rate": 0.1,
  "latency_ms": { "avg": 1830, "p50": 1200, "p95": 5100 },
  "by_status": [
    { "provider": "dashscope", "status": "200", "count": 90 },
    { "provider": "budget", "status": "429", "count": 10 }
  ]
}
```

约定：

- `error_rate = 非 2xx 计数 / requests_total`，无请求时为 0。
- 延迟来自 `gateway_request_duration_seconds` 直方图，用桶边界线性插值估算 p50/p95；无数据时三个值为 0。
- 仅统计已埋点的 chat/embeddings 主链路（预算 429 已计入 `budget/429`）。

## 4. 后端设计

### 4.1 `internal/usage`

新增类型与方法（`Repo` 接口扩展；当前仅 `sqliteRepo` 一个实现，安全）：

```go
type Totals struct { Requests, PromptTokens, CompletionTokens, CacheHits int; Cost, Saved float64 }
type Group struct { Key string; Totals }
type Bucket struct { Ts string; Requests, PromptTokens, CompletionTokens int; Cost float64 }

type Summary struct {
    Totals     Totals
    ByModel    []Group
    ByProvider []Group
    ByDay      []Bucket
    ByHour     []Bucket
}

type Query struct { Model, Provider string; Cache *bool; Q string; Limit, Offset int }

func (r *sqliteRepo) Summary(ctx, month string) (Summary, error)
func (r *sqliteRepo) Query(ctx, q Query) (total int, records []Record, err error)
```

SQL 要点：

- 均按 `substr(created_at,1,7) = month` 过滤月度聚合。
- `by_day`：`substr(created_at,1,10)` 分组；`by_hour`：`substr(created_at,1,13) || ':00'` 分组，限定 `created_at >= now-48h`。
- `cache_hits`：`SUM(cache_hit)`。
- 全部使用参数化查询。
- 迁移：`CREATE INDEX IF NOT EXISTS idx_usage_created_at ON usage_records(created_at)`、`idx_usage_model ON usage_records(model)`。

### 4.2 `internal/metrics`

新增：

```go
type Snapshot struct {
    RequestsTotal int
    ErrorRate     float64
    LatencyMS     struct{ Avg, P50, P95 float64 }
    ByStatus      []StatusCount // {Provider, Status string; Count int}
}
func (m *Registry) Snapshot() Snapshot
```

通过 `m.reg.Gather()` 读取 `gateway_requests_total` 与 `gateway_request_duration_seconds`。

### 4.3 `internal/api`

- 新增 `summary.go`、`requests.go`、`health.go`，各注册一个 GET 路由。
- `summary` 需要 `config.Settings`（预算参数）与 `repo`；`health` 需要 `metrics.Registry`。
- `router.go` 构造时把 `m := metrics.New()` 与 `s` 传给新 register 函数（`m` 目前是局部变量）。
- 现有 `admin.go` 的 `/v1/admin/usage` 保留，供 SSE 快照与兼容。

### 4.4 图表库本地化

- 新增 `internal/api/web/chart.umd.min.js`（来源 `https://cdn.jsdelivr.net/npm/chart.js@4.4.9/dist/chart.umd.min.js`）。
- `dashboard.go` 增加 `//go:embed web/chart.umd.min.js`，注册 `GET /assets/chart.js`，响应头 `Cache-Control: public, max-age=31536000, immutable`。
- `index.html` 改为 `<script src="/assets/chart.js"></script>`，删除 CDN 引用。

## 5. 前端设计

单页，纯 HTML/CSS/JS，`web/index.html`。

布局（自上而下）：

1. 顶栏：标题、更新于、连接状态点、刷新按钮；token 输入框在未保存时可见。
2. KPI 卡片行：本月花费、本月节省、请求数、缓存命中率、总 tokens。
3. 预算条：已用 / 额度进度条；`limit=0` 显示「预算限制已关闭」；`degraded` 黄色、`rejected` 红色。
4. 图表区：
   - 趋势图：左上切换「按天 / 按小时」；按天时叠加今日/近7天/近30天/本月范围；数据集为成本（柱，左轴）与 tokens（线，右轴）。
   - 模型成本占比环形图。
   - 供应商占比（环形或横条）。
5. 健康卡：请求总数、错误率、p50/p95/avg、状态分布小表。
6. 明细表：筛选下拉（模型、供应商、缓存）+ 搜索框 + 上一页/下一页 + 总数；列沿用现值并保留实时插入。

交互：

- 首次加载：从 `localStorage` 或 `?token=` 取 token；拉取 summary、requests、health，并连接 SSE。
- SSE 收到新记录：防抖 800ms 后重拉 summary 与 health；若当前在第 1 页且记录匹配筛选条件，则插入表格并同步 `total`。
- 翻页/筛选/搜索：走 `/v1/admin/requests`，不整页刷新。
- 10 分钟兜底轮询保留（页面隐藏时暂停）。
- 所有文本用 `textContent` 写入，避免 XSS。

## 6. 错误处理与边界

- 未填 / 错误 token：接口 401，页面提示「token 错误或未填」。
- `month` 非法：400；前端只传合法值。
- 空数据：图表显示空态，表格显示「暂无数据」。
- `limit=0`：预算条显示已关闭，不显示百分比。
- SSE 断开：显示「重连中」，3 秒后重试。
- Chart.js 加载失败：页面其余部分仍可用（图表区显示占位）。

## 7. 测试

- `internal/usage`：总量、按模型/供应商、按天/按小时聚合、筛选与分页、`cache` 过滤、`q` 搜索。
- `internal/api`：summary（含 `limit=0` 与正常预算两种）、requests（筛选/分页/非法参数）、health（空 registry 与有数据）。
- `internal/metrics`：Snapshot 的计数汇总与分位数估算。
- 现有测试保持通过：`go test ./...`、`go vet ./...`、`go build ./...`。

## 8. 验收标准

1. 打开 `http://localhost:8080/` 不再发起任何外部网络请求即可正常绘图（无 jsdelivr 依赖）。
2. 五类信息全部可见：成本分析、预算监控、运行健康、趋势（按天+按小时）、明细筛选/分页。
3. SSE 新调用后 1 秒内 KPI 与趋势更新（防抖后）。
4. 当前 `.env` 为 `BUDGET_MONTHLY_LIMIT=0` 时预算条显示「预算限制已关闭」。
5. `go test ./...` 全绿。

## 9. 文件变更

修改：

- `internal/usage/usage.go`、`internal/usage/sqlite.go`
- `internal/metrics/metrics.go`
- `internal/api/router.go`、`internal/api/dashboard.go`
- `internal/api/web/index.html`

新增：

- `internal/api/summary.go`、`internal/api/requests.go`、`internal/api/health.go`
- `internal/api/web/chart.umd.min.js`
- 对应 `_test.go`