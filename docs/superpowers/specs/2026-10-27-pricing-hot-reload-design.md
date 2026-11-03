# 设计：价格表分时计价与远程热更新

日期：2026-10-27
状态：已批准

## 背景

当前 `internal/pricing/pricing.go` 是一张写死在代码里的 `map[string]rate`，改价必须改代码并重新编译，太麻烦。另外 `deepseek-v4.1-flash` 是分时计价：高峰（输入 2 / 输出 8 元每百万 token）、空闲（输入 1 / 输出 4），空闲时段为北京时间 00:30–08:30。

## 目标

1. 价格表支持"高峰 / 空闲"分时价，按**请求发生时刻**（时区 `Asia/Shanghai`）判定。
2. 价格表可从远程 URL 拉取 JSON，**启动时 + 定时热更新**，无需重编译。
3. 内置一份默认价格表（`go:embed`），离线可用、开箱有价。
4. 加入 `deepseek-v4.1-flash`。

## 非目标

- 不爬取阿里云定价页；"自动更新"= 用户/社区维护一份 JSON，网关定时拉取。
- 不做历史价归档、账单对账、按 provider 账户区分价。

## 设计

### 1. 数据模型（`internal/pricing`）

远程与内置 JSON 格式（单位：元 / 百万 token）：

```json
{
  "models": {
    "qwen3.8-flash": { "in": 0.8, "out": 2.7 },
    "deepseek-v4.1-flash": {
      "in": 2.0, "out": 8.0,
      "off_peak": { "in": 1.0, "out": 4.0, "start": "00:30", "end": "08:30" }
    }
  }
}
```

Go 类型：

```go
type rate struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

type modelPrice struct {
	In      float64 `json:"in"`
	Out     float64 `json:"out"`
	OffPeak *offPeak `json:"off_peak,omitempty"`
}

type offPeak struct {
	In    float64 `json:"in"`
	Out   float64 `json:"out"`
	Start string  `json:"start"` // "HH:MM"
	End   string  `json:"end"`   // "HH:MM"，可跨午夜（start > end）
}

type priceDoc struct {
	Models map[string]modelPrice `json:"models"`
}
```

- `in/out` = **高峰**价（默认价）。
- `off_peak` 可选：命中时间窗时用空闲价；`start`/`end` 缺省为 `00:30`/`08:30`。
- 内置默认 `default.json`（`//go:embed`）包含现有全部模型 + `deepseek-v4.1-flash`。

### 2. 计价

- 包级默认表 `store`（可被远程热更新替换）。
- `Cost(model string, promptTokens, completionTokens int) float64`：按 `NowFunc()` 的时刻选 peak/off-peak；未知模型返回 0（保持现状）。
- `Score(model string) (float64, bool)`：用**高峰**价 `in+out`（降级候选排序更保守）。
- `NowFunc var = time.Now`（测试可注入）。
- 时区：`Location` 在 init 用 `time.LoadLocation("Asia/Shanghai")`；导入 `_ "time/tzdata"` 内嵌时区库，保证 Windows 正确。

时间窗判定：将 `now.In(Location)` 转成当日分钟数 `m`；把 start/end 也转分钟 `s/e`。若 `s<=e`：命中 `s<=m<e`；若 `s>e`（跨午夜）：命中 `m>=s || m<e`。

### 3. 远程热更新

```go
type Store struct {
	mu          sync.RWMutex
	models      map[string]modelPrice // 已解析（含内置）
	lastRefresh time.Time
	source      string
	lastErr     string
}

func Refresh(ctx context.Context, url string) error // 拉取+校验+merge+原子替换
func StartAutoRefresh(url string, interval time.Duration) // url=="" 时 no-op
func Snapshot() (map[string]modelPrice, time.Time, string, string) // 供 admin 接口
```

- `Refresh`：`GET url` → 仅允许 `http`/`https` scheme → 10s 超时 → 读上限 1MB → `json.Unmarshal` → 校验每条：`in>=0 && out>=0`，off_peak 的 start/end 可解析（`15:04`）→ **merge**：以「内置默认表」为底，远程同名覆盖/新增 → 原子替换 `models`，更新 `lastRefresh`、`source`。
- 失败语义：任一步失败**保留上一份** `models`，记录 `lastErr`，返回 error（调用方日志）。
- `StartAutoRefresh`：url 为空直接返回；否则起 goroutine：立即 `Refresh`，然后 `time.Ticker(interval)` 周期刷新；每次失败只记日志。
- merge 语义说明：远程删除某模型不会移除内置价（回退到内置）。要"删除"，远程把该模型置为 0 或从内置表移除。

### 4. 配置（`internal/config`）

- `Settings.PricingURL string` ← `PRICING_URL`（默认空）。
- `Settings.PricingRefreshInterval time.Duration` ← `PRICING_REFRESH_INTERVAL`（Go duration 字符串，如 `6h`；解析失败或为空默认 `6h`）。

### 5. 装配与接口

- `cmd/server/main.go`：`config.Load()` 后调用 `pricing.StartAutoRefresh(s.PricingURL, s.PricingRefreshInterval)`。
- 新增 `GET /v1/admin/pricing`（鉴权，归入 `registerAdmin` 所在组）：返回 `{ "source": ..., "last_refresh": ..., "last_error": ..., "models": {...} }`。

### 6. 兼容

- `Cost` / `Score` 签名不变，调用点（`router.go` 的 `recordUsage`/`recordCacheHit`、`modelCandidates`、降级）无需改。
- 现有模型（`deepseek-chat`、`gpt-4o-mini`、`qwen-turbo`、`qwen-plus`、`qwen-max`、`qwen3.8-flash`）全部保留在内置表。

## 测试策略（TDD）

1. 分时选价：固定 `NowFunc` 到 03:00（空闲）与 12:00（高峰），断言 `deepseek-v4.1-flash` 的 `Cost` 分别用 1/4 与 2/8。
2. 边界：00:29（高峰）、00:30（空闲）、08:29（空闲）、08:30（高峰）。
3. 跨午夜窗口（如 22:00–06:00）判定。
4. `Score` 用高峰价（`deepseek-v4.1-flash` = 10）。
5. 内置默认表含全部既有模型。
6. `Refresh`：用 `httptest` 起假价格服务，验证解析 + merge（覆盖既有模型、新增新模型）；非 http scheme 被拒；非 2xx 报错；非法 JSON 报错；失败保留旧表。
7. `Snapshot` 反映 source/lastRefresh。

## 验收

- `go test ./...` 全绿；`gofmt`/`vet` 干净。
- 本地起网关，`GET /v1/admin/pricing` 能看到内置表；把 `PRICING_URL` 指向本地一个 JSON（改价/加模型）后重启，`Cost` 按新价计算、`/v1/admin/pricing` 的 source 与 last_refresh 更新。
- `deepseek-v4.1-flash` 在 12:00 计高峰、03:00 计空闲（可用 `NowFunc` 或直接看 `Cost` 单测）。