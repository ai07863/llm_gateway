# 设计：llm-gateway P0 缺陷修复

日期：2026-10-26
状态：已批准（方案 A）

## 背景

代码审阅发现三处 P0 级正确性缺陷，本次集中修复：

1. **缓存命中完全不落库**：命中在 `recordUsage` 之前 return，且 `usage.Record.CacheHit` 从未置位，导致"缓存省了多少钱"完全不可见。
2. **降级模型映射错误**：`budget.CheaperModel` 无脑返回 `deepseek-chat`，当 deepseek 未配置时会把非法模型名发给其它 provider，导致 400/502。
3. **故障转移不分类、无重试**：任何错误（含 400/401）都会切换 provider；README 声称的重试未实现。

## 目标

- 缓存命中可计量：记录命中次数与节省金额。
- 降级只在"存在更便宜的、已配置的模型"时发生，且目标合法。
- 故障转移只在可重试错误上发生，并对瞬时错误做有界指数退避重试。

## 非目标（本次不做）

- 价格表外置/可配置（P1）
- 语义缓存性能优化（P1）
- OpenAI 参数透传、任意 provider 配置化、优雅退出、访问日志、`/metrics` 鉴权（P2）
- 跨 provider 的"同名模型"故障转移（本项目 provider 模型名互不相同，改为按各自模型切换）

## 设计

### 1. 缓存命中落库（记命中 + 节省金额）

**缓存层**
- `cache.Exact` 条目由 `{value, expires}` 扩展为 `{content string, promptTokens, completionTokens int, expires time.Time}`。
- `Exact.Set(key string, content string, promptTokens, completionTokens int)`。
- `Exact.Get(key string) (CachedResult, bool)`，`CachedResult{Content string; PromptTokens int; CompletionTokens int}`。
- `cache.Semantic` 条目增加 `promptTokens`、`completionTokens`；`Store(scope string, qvec []float64, content string, promptTokens, completionTokens int)`；`Lookup(...) (CachedResult, []float64, bool)`。

**计量层**
- `usage.Record` 增加字段 `SavedCost float64`（json `saved_cost`）。
- `usage.Repo` 增加 `MonthlySaved(ctx, yyyymm) (float64, error)`。
- SQLite：`usage_records` 增加列 `saved_cost REAL`；对已存在的旧库执行幂等迁移（`CREATE TABLE IF NOT EXISTS` 保持新增列，另对旧表 `ALTER TABLE ... ADD COLUMN saved_cost REAL`，忽略 "duplicate column" 错误）。`MonthlySaved` 用 `SUM(saved_cost)`。

**编排（api/router.go）**
- 非流式命中（精确或语义）：计算 `saved := pricing.Cost(req.Model, r.PromptTokens, r.CompletionTokens)`，落库 `usage.Record{Model: req.Model, Provider: "cache", PromptTokens: r.PromptTokens, CompletionTokens: r.CompletionTokens, Cost: 0, SavedCost: saved, CacheHit: true}`，返回内容。
- 流式精确命中：同上（流式无语义缓存）。
- 未命中成功时，写回缓存时带上 `resp.PromptTokens / resp.CompletionTokens`。
- `recordUsage` 改为接收完整 `usage.Record`（含 `CacheHit`、`SavedCost`），减少参数漂移。

**API**
- `/v1/admin/usage` 响应增加 `month_saved`；记录中已含 `cache_hit`，新增 `saved_cost`。
- 看板 `index.html` 增加"本月节省：X 元"显示。

**指标**
- 命中沿用 `gateway_requests_total{provider="cache",status="hit"}`。

### 2. 降级模型映射（基于已配置模型）

- `budget` 包保持纯函数、不依赖 `pricing`：`CheaperModel(current string, currentPrice float64, candidates []Model) string`，其中 `Model{Name string; Price float64}`，`Price<=0` 表示价格未知。
- 选择规则：取 `candidates` 中 `Price>0` 且最小者；仅当 `currentPrice>0` 且 `currentPrice > 目标价格` 时返回目标名，否则返回 `current`（不降级）。
- 未知价格的候选（`Price<=0`）不可选；`currentPrice<=0`（当前模型价格未知）时**不降级**（保守）。
- `api/router.go` 从 `s.Providers` 构造候选：对每个已配置的 chat 模型查询 `pricing.Cost` 的价格度量（`in + out` 元/百万 token），未知价跳过。
- 降级后，目标模型成为主转发 target（见下节路由），保证由拥有该模型的 provider 承接。

### 3. 故障转移：错误分类 + 重试 + 模型感知路由

**provider 层**
- 新增类型：
  ```go
  type Error struct {
      Provider   string
      StatusCode int
      Retryable  bool
      Err        error
  }
  func (e *Error) Error() string
  func (e *Error) Unwrap() error
  ```
- `openai_compat` 在 `res.StatusCode >= 400` 时返回 `*provider.Error`：`Retryable = StatusCode == 429 || StatusCode >= 500`；其余 4xx 不可重试。网络/超时等 `client.Do` 错误包装为 `Retryable=true`。
- 提供分类助手：`provider.IsRetryable(err error) bool`（非 `*Error` 的未知错误默认**可重试**，覆盖网络类错误）。该助手仅用于故障转移的**建流前**错误；流式 `[DONE]` 缺失属于流中途错误，不会进入故障转移路径。

**proxy 层**
- 引入
  ```go
  type Target struct {
      Provider provider.Provider
      Model    string
  }
  ```
- `ChatWithFailover(ctx, targets []Target, req ChatRequest) (ChatResponse, provider.Provider, error)`：
  - 依次遍历 targets；对每个 target 设置 `req.Model = target.Model`。
  - 单 target 内对可重试错误重试至多 `MaxAttempts` 次（指数退避 `RetryBackoff`）。
  - 不可重试错误：立即返回，不再尝试后续 target。
  - target 用尽：返回最后一次错误。
- `StreamWithFailover` 同构（仅覆盖建流前的错误；流中途错误不重试，因为字节已发出）。
- 可注入变量 `var MaxAttempts = 3`、`var RetryBackoff = 200 * time.Millisecond`（便于测试注入，避免慢测）。

**路由（api/router.go）**
- 构造 `targetsForModel(model string) []proxy.Target`：
  - 主 target：拥有 `model` 的 provider（配置中 `ProviderConfig.Model == model`）；
  - 备用 target：其余 provider，各自使用**自己配置的模型**（`ProviderConfig.Model`）。
  - 若 `model` 未被任何 provider 配置：主 target = 第一个 provider 且 `Model = 请求的 model`（保持旧行为），备用 target 仍用各自模型。
- 请求路径：非流式/流式都先 `targets := targetsForModel(req.Model)`，再调用 `ChatWithFailover / StreamWithFailover`。
- 降级：先算出降级目标模型，改写 `req.Model`，再据此构造 targets。

## 数据流（更新后）

`鉴权 → request_id → 精确缓存(命中: 落库 saved_cost) → 语义缓存(命中: 落库 saved_cost) → 预算(放行/降级: 选已配置便宜模型/拒绝) → 模型感知路由(主=拥有该模型的 provider, 备用=各用自身模型) → 调用(可重试错误在同 provider 退避重试, 用尽再切) → 输出 → 落库(带 token 与 cache_hit) + 指标 + 写回缓存(带 token)`

## 影响面 / 兼容

- 变更 `provider`（新增 Error 类型）、`proxy`（Target、签名、重试）、`cache`（Set/Get/Lookup/Store 签名）、`usage`（Record 字段、Repo 方法）、`api/router.go`、`api/admin.go`、`index.html`。
- 现有测试需按新语义更新：
  - `proxy/failover_test.go`：改用 `[]Target`；新增"可重试→重试后切备份""不可重试→不切"用例；注入 `MaxAttempts=1` 加速。
  - `api/budget_test.go`：降级测试改为配置两个模型（deepseek-chat + gpt-4o-mini），断言降级到已配置的 deepseek-chat。
  - `cache/*_test.go`、`usage/sqlite_test.go`：补充 token 与 saved_cost 用例。
- `tests/integration_test.go` 预期不受影响（单 provider，模型为 "m"）。

## 测试策略（TDD，先写失败测试）

1. `cache`：Set/Get 与 Store/Lookup 能带回 token 数。
2. `usage`：`SavedCost` 正确持久化；`MonthlySaved` 求和；旧表迁移不报错且新列可用。
3. `proxy`：可重试错误重试 N 次后切备份；不可重试错误不切、直接失败；空 targets 报错。
4. `provider`：4xx→不可重试，429/5xx→可重试，网络错误→可重试。
5. `budget`：存在更便宜已配置模型→降级；无更便宜/未知价→不降级。
6. `api`：命中落库一条 `cache_hit=true` 且 `saved_cost>0`；备用 target 使用其自身模型。

## 验收

- `go test ./...` 全绿。
- 用本地 mock 上游复现：重复同请求后 `/v1/admin/usage` 出现 `cache_hit=true` 且 `saved_cost>0` 的记录，`month_saved>0`。