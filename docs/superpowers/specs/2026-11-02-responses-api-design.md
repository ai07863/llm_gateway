# Responses API 透传支持设计

日期：2026-11-02
状态：已确认（架构与行为经用户确认）

## 背景

网关目前提供 `/v1/chat/completions`、`/v1/embeddings`、`/v1/models`。较新的
Codex CLI 默认使用 OpenAI Responses API（`wire_api = "responses"`），而 Codex
0.81 起已弃用 `wire_api = "chat"`，缺少 `/v1/responses` 会阻塞升级。

2026-11-02 实测 DashScope 兼容端点
`https://dashscope.aliyuncs.com/compatible-mode/v1/responses` 已原生支持：

- 非流式请求返回标准 `object: "response"` 与 `usage.input_tokens/output_tokens`；
- `stream: true` 返回完整 SSE 事件（`response.created` … `response.completed`）；
- function tools 请求返回 `function_call` output item。

因此采用**透明透传**，不把 Responses 翻译成 Chat Completions。

## 目标

- 新增 `POST /v1/responses`，支持非流式与流式。
- 复用现有鉴权、模型归属路由、失败转移、预算检查/降级、usage 记账、成本计算、
  看板 SSE 推送与 metrics。
- Codex 0.166（`wire_api = "responses"`）可经网关完成对话与工具调用。

## 非目标

- 不实现缓存（有状态工具循环与 Responses 语义下缓存不安全）。
- 不实现完整 Responses 规范；`previous_response_id`、`store`、files、内置工具等
  由上游行为决定，网关不做本地状态管理。
- 不实现 Responses → Chat 翻译层。
- 不改变现有 `/v1/chat/completions` 行为。
- 不新增环境变量或配置项。

## 架构

| 组件 | 变更 |
|---|---|
| `internal/provider` | 新增可选接口 `ResponsesProvider`（`Responses`、`ResponsesStream`），由 `openAICompat` 实现；不加入现有 `Provider` 接口，避免测试 fake 被迫改造 |
| `internal/proxy` | 新增 `ResponsesWithFailover`、`ResponsesStreamWithFailover`，复用 `Target`、重试分类与退避 |
| `internal/api` | 新增 `responses.go`，注册 `POST /v1/responses`，负责解析、路由、预算、记账、SSE 转发 |
| `internal/api/router.go` | 构造 handler 依赖（providers、repo、metrics、budget 候选、`targetsForModel`）并注册 |
| `README.md` | 端点表补充 `/v1/responses`，说明 Codex 可选用 responses 协议 |

数据流：

```
client → 鉴权 → 解析 model/stream → 预算 → targetsForModel
       → upstream /responses → 原样回传 → 提取 usage → 写库/推送看板 + metrics
```

### Provider 接口

```go
type ResponsesRequest struct {
    Model  string
    Stream bool
    Raw    []byte // 客户端原始 body；实现方只改写 model
}

type ResponsesResult struct {
    Model            string
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    Raw              []byte // 上游响应体，原样返回
}

type ResponsesChunk struct {
    Raw              string // 完整 SSE 帧（含结尾空行），原样转发
    Done             bool
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    Err              error
}

type ResponsesProvider interface {
    Responses(ctx context.Context, req ResponsesRequest) (ResponsesResult, error)
    ResponsesStream(ctx context.Context, req ResponsesRequest) (<-chan ResponsesChunk, error)
}
```

## 协议处理

### 请求

- 读取原始 body，解析 `model`（必填）与 `stream`（缺省 false）。
- 保持其余字段逐字节语义不变，仅将 JSON 顶层的 `model` 改写为当前 target 的模型；
  字段顺序与未知字段不影响上游。
- 模型归属：使用现有 `targetsForModel(model)`；归属 provider 优先，其后为其他
  provider（沿用现有行为）。
- 预算：请求前查询当月成本并调用 `budget.Evaluate`；`Reject` 返回 429 且不调上游；
  `Degrade` 使用 `budget.CheaperModel` 改写模型后重新路由。
- 无可用 provider：返回 502。

### 非流式响应

- 上游 2xx：读取完整 body，按 `usage.input_tokens`、`usage.output_tokens`、
  `usage.total_tokens` 记账，原样返回 body（`application/json`）。
- 上游 4xx：返回与上游相同的状态码和 body，便于客户端看到真实错误；不记账。
- 上游 5xx 或网络错误：按可重试规则重试/切换 target；耗尽后返回 502
  `{"error":{"message":"..."}}`；不记账。

### 流式响应

- 设置 `Content-Type: text/event-stream`、`Cache-Control: no-cache`、
  `X-Accel-Buffering: no`。
- 按空行分隔切分 SSE 帧，逐帧原样写出并 flush，包括 `id:`、`event:`、
  `:HTTP_STATUS/200` 注释行与 `data:` 行；不追加 `[DONE]`（Responses 客户端不需要）。
- 从 `data:` JSON 中识别终态事件，读取
  `response.usage.input_tokens/output_tokens/total_tokens`：
  - `response.completed`、`response.incomplete`：终态成功，记账并记录 200；
  - `response.failed`：终态失败，向客户端写 error 事件（消息取
    `response.error.message`，缺省为 `"response.failed"`），记录 500，不记账。
- 收到终态事件并转发完该帧后立即停止读取上游，不等待连接 EOF。
- 中途读流错误：向客户端写出 `event: error` + `data: {"type":"error","message":"..."}`
  后关闭；记录 500，不记账。
- 客户端断开：停止读取，记录 `canceled`，不记账。
- 失败转移只在尚未向客户端输出首帧前生效；输出首帧后不再切换 provider。

### 记账与指标

- token 映射：`input_tokens → PromptTokens`，`output_tokens → CompletionTokens`，
  `total_tokens → TotalTokens`。
- 成本：`pricing.Cost(effectiveModel, prompt, completion)`，沿用高峰/闲时定价。
- 记录：`Model = 实际生效模型`（预算降级后），`Provider = 实际使用的 provider`，
  `CacheHit = false`。
- metrics：沿用现有 `Requests`、`Latency`、`Tokens` 标签；状态使用
  `200`、`429`、`500`、`canceled` 与 provider 名。

## 错误处理汇总

| 场景 | 客户端可见 | 是否记账 |
|---|---|---|
| 上游 200 非流式 | 上游 body | 是 |
| 上游 4xx | 上游状态码 + body | 否 |
| 上游 5xx/网络，重试耗尽 | 502 JSON error | 否 |
| 流式正常完成（`completed` / `incomplete`） | 上游 SSE 帧 | 是 |
| 流式终态失败（`failed`） | 上游已发帧 + error 事件 | 否 |
| 流式中途错误 | 上游已发帧 + error 事件 | 否 |
| 客户端断开 | 连接关闭 | 否 |
| 预算拒绝 | 429 JSON error | 否 |

若 Codex 实际请求中的 `developer` role 被上游拒绝，再补充 `input[].role`
从 `developer` 到 `system` 的归一化；当前不预先实现。

## 测试

### 单元测试

- provider：只改写 `model`，未知字段保留；非流式 usage 解析；SSE 分帧与
  `response.completed` usage 提取；4xx 不可重试、429/5xx 可重试。
- api：非流式透传与记账；流式帧保真（`id:`/`event:`/`:HTTP_STATUS/200`）与记账；
  400 不切换 provider；500 切换至下一 provider；预算拒绝 429；缺少 token 401。

### 全量验证

- `gofmt`、`go vet ./...`、`go test ./...` 全绿。

### 线上验收

- 临时实例（8091）连接真实 DashScope：非流式与流式 `/v1/responses` 均成功，
  `usage_records` 出现对应记录。
- 安装 `@openai/codex@0.166.0` 到临时目录，使用独立 `CODEX_HOME` 与
  `wire_api = "responses"`，指向临时网关：
  - `codex exec "reply with one word: ok"` 成功；
  - 一次 shell 工具调用（如创建临时文件）完成完整 tool call 循环；
  - 网关记录对应 usage。

## 验收标准

- [ ] `POST /v1/responses` 非流式与流式均可用，SSE 帧原样保留。
- [ ] 模型路由、预算拒绝/降级、失败转移行为与 chat 路径一致。
- [ ] usage 与成本正确落库，看板与 `/v1/admin/usage` 可查询。
- [ ] Codex 0.166 使用 `responses` 协议可完成对话与工具调用。
- [ ] 现有测试全绿，README 已更新。