# llm-gateway

一个用 Go 编写的 **LLM 统一网关**：提供 OpenAI 兼容的统一入口，把请求转发到多个大模型 provider，并附带成本可见性与预算护栏。

## 为什么做这个（动机）

我在多个项目里同时使用 DeepSeek、OpenAI、通义、智谱等大模型 API，实际痛点很集中：

1. **配置分散**：每个 provider 的 key、base_url、调用方式散落在各个项目里，改一处要动多处。
2. **成本不透明**：每次调用花了多少钱、用了多少 token，完全看不到。
3. **重复造轮子**：重试、超时、限流、故障切换，每个项目都要重写一遍。
4. **重复付费**：相同或相似的问题反复问，重复花钱。

于是我做了这个网关：**所有 AI 项目都指向它**——既能统一转发到任意 provider，又能看到成本、控制花费，并在主 provider 挂掉时自动切换。

## 功能特性

- **OpenAI 兼容 API**：`POST /v1/chat/completions`（支持 SSE 流式）、`POST /v1/embeddings`、`GET /v1/models`。
- **多 provider 转发 + 故障转移**：主 provider 失败后自动切换到备用 provider，全部失败返回 `502`。
- **成本 / Token 落库**：每次调用写入 SQLite，可通过 `/v1/admin/usage` 查询历史用量与当月成本。
- **预算护栏**：当月花费接近上限时**降级**到便宜模型，超上限时**拒绝**（`429`）。
- **降本缓存**：精确缓存（request hash）+ 语义缓存（embedding 余弦相似度）。
- **可观测**：Prometheus `/metrics` + 极简用量看板页 `/`。
- **固定 token 鉴权**：防止网关裸奔，`Authorization: Bearer <GATEWAY_TOKEN>`。

## 架构

```
[上游: AI 应用 / OpenAI SDK / curl]
        │  OpenAI 兼容 REST + SSE
┌───────▼──────────────────────────────────────┐
│  API 层 (Gin)                                   │
│   POST /v1/chat/completions  (SSE)              │
│   GET  /v1/models                               │
│   GET  /v1/admin/usage  GET /metrics  /healthz  │
│   GET  /  (用量看板)                             │
│   中间件: 鉴权(固定 token) / RequestID / recover │
├───────────────────────────────────────────────┤
│  业务编排                                       │
│   缓存(精确→语义) → 预算(放行/降级/拒绝)         │
│   → 路由 + 故障转移 → 调用上游                   │
│   → 计量落库(SQLite) + 指标(Prometheus)          │
├───────────────────────────────────────────────┤
│  基础设施                                       │
│   Provider(OpenAI 兼容, stdlib net/http)        │
│   UsageRepo(modernc.org/sqlite)  Metrics        │
└───────────────────────────────────────────────┘
```

一次请求的数据流：**鉴权 → request_id → 精确缓存 → 语义缓存 → 预算校验 → 路由 → 调用(失败则重试/切换备用) → 输出 → 落库 + 打指标 + 写回缓存**。

目录结构：

```
llm-gateway/
  cmd/server/main.go        # 装配依赖 + 启动 HTTP
  internal/
    api/                    # Gin handler + 中间件 + 内嵌看板
    proxy/                  # 转发编排 + 故障转移
    provider/               # OpenAI 兼容适配 (接口 + 实现)
    cache/                  # 精确缓存 + 语义缓存
    budget/                 # 预算规则
    usage/                  # SQLite 记录 + 查询
    metrics/                # Prometheus 指标
    pricing/                # 模型单价表
    config/                 # 配置加载
  configs/.env.example      # 配置样例
  tests/                    # 集成测试
  Dockerfile
  docker-compose.yml
```

## 快速开始

### 最快上手（TL;DR）

```bash
cp configs/.env.example .env              # 填 DEEPSEEK_API_KEY 和自定义 GATEWAY_TOKEN
docker compose up --build                 # 启动网关，监听 :8080
pip install openai && python examples/python_openai_client.py   # 跑示例（先设 GATEWAY_TOKEN）
```

### 1. 准备配置

```bash
cp configs/.env.example .env
```

编辑 `.env`，至少填入一个 provider 的 key，并把 `GATEWAY_TOKEN` 改成你自己的：

```dotenv
GATEWAY_TOKEN=change-me
DEEPSEEK_API_KEY=sk-xxxxxxxx
OPENAI_API_KEY=sk-xxxxxxxx
```

### 2. 用 Docker 一键启动

```bash
docker compose up --build
```

服务监听 `http://localhost:8080`。数据（SQLite）持久化在命名卷 `gateway_data` 中，容器内路径 `/app/data/gateway.db`。

> 构建时默认使用 `GOPROXY=https://goproxy.cn,direct`（便于国内网络拉取依赖）。若处于其它网络环境，可用环境变量覆盖：
> `GOPROXY=https://proxy.golang.org,direct docker compose build`。

### 3. 本地运行（不用 Docker）

```bash
go run ./cmd/server
```

配置全部来自环境变量（`GATEWAY_TOKEN`、`DB_PATH`、`*_API_KEY` 等）。启动时若当前工作目录存在 `.env`，会被自动加载；不存在则直接从 shell 环境读取。当前**不**支持 YAML 配置文件（`configs/config.yaml` 尚未实现）。

## API 示例

### 非流式

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "messages": [{"role": "user", "content": "用一句话解释什么是网关"}]
  }'
```

### 流式（SSE）

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "stream": true,
    "messages": [{"role": "user", "content": "写一个 Go 的 hello world"}]
  }'
```

请求体与响应体均与 OpenAI Chat Completions 兼容，因此可以直接把 OpenAI SDK 的 `base_url` 指向本网关。

### 其他端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/v1/chat/completions` | 对话补全（`stream: true` 时走 SSE） |
| POST | `/v1/embeddings` | 文本向量化（`input` 可以是字符串或字符串数组） |
| GET | `/v1/models` | 列出已配置的模型 |
| GET | `/v1/admin/usage` | 用量查询：当月成本 `month_cost` + 最近记录 `records` |
| GET | `/metrics` | Prometheus 指标 |
| GET | `/` | 极简用量看板页 |
| GET | `/healthz` | 健康检查 |

`/v1/admin/usage` 示例：

```bash
curl http://localhost:8080/v1/admin/usage -H "Authorization: Bearer change-me"
```

```json
{
  "month_cost": 0.0123,
  "records": [
    {"id": "...", "model": "deepseek-chat", "provider": "deepseek",
     "prompt_tokens": 20, "completion_tokens": 40, "cost": 0.00003, "created_at": "..."}
  ]
}
```

`/metrics` 暴露的关键指标：

- `gateway_requests_total{provider,status}`：按 provider / 状态码的请求计数。
- `gateway_request_duration_seconds{provider}`：调用延迟直方图。
- `gateway_tokens_total{provider,kind}`：按 provider 累计的 prompt / completion token。

## 接入自己的项目

网关是 OpenAI 兼容的，所以任何支持自定义 `base_url` 的客户端都能接入：**base_url 指向网关，api_key 填 `GATEWAY_TOKEN`**。

Python：

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",   # 指向网关
    api_key="my-secret-123",               # 即你的 GATEWAY_TOKEN
)

resp = client.chat.completions.create(
    model="deepseek-chat",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

Node.js：

```js
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:8080/v1",
  apiKey: process.env.GATEWAY_TOKEN,
});

const resp = await client.chat.completions.create({
  model: "deepseek-chat",
  messages: [{ role: "user", content: "你好" }],
});
console.log(resp.choices[0].message.content);
```

完整可运行的最小示例见 [`examples/python_openai_client.py`](examples/python_openai_client.py)。

接入后，你的多个 AI 项目都只需指向这一个网关，无需各自保存 provider 的 key；成本、预算、缓存与故障转移都由网关统一处理。

## 测试与验收

```bash
go test ./...
```

- 领域逻辑（预算规则、成本计算、缓存匹配、故障转移）有单元测试。
- `tests/integration_test.go` 用一个 `httptest` 假上游跑通全链路：`NewRouter` 装配 → 鉴权 → 转发到 fake provider → 断言返回内容。

### 手动验收清单

1. `docker compose up --build`，`curl /healthz` 返回 `{"status":"ok"}`。
2. 用真实 key 发一次非流式 + 一次流式请求，确认有回复。
3. 访问 `/v1/admin/usage`，确认出现用量记录与当月成本。
4. 访问 `/metrics`，确认 `gateway_requests_total` 等指标有数。
5. 打开 `/`，确认看板能出图。

## 技术栈与取舍

- **Go + Gin**：唯一 Web 框架，SSE 用 `c.Stream`。
- **上游转发用标准库 `net/http`**：直接反向代理，不引任何 LLM SDK。
- **`modernc.org/sqlite`（纯 Go，无 CGO）**：单文件、零依赖，容器内 `CGO_ENABLED=0` 也能跑。
- **不引 Redis / ORM**：单用户场景 SQLite + 内存足够，减少部署依赖。
- **单用户定位**：不做多租户 / 计费后台，把精力集中在网关核心与成本护栏。