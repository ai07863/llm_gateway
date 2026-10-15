# LLM 统一网关（llm-gateway）— 设计文档

- 日期：2026-10-15
- 代号：`llm-gateway`（可重命名）
- 定位：Go 后端简历项目，面向 **AI 应用开发 / 后端开发** 实习岗
- 使用者：单用户（作者本人）
- 时间预算：充裕，单人
- 关系说明：本项目**全新独立实现**，不与已归档的 `rag-kb` 共用代码

---

## 1. 背景与创作意图

### 1.1 真实痛点（面试"为什么做"的答案）

作者同时使用多个大模型 API（DeepSeek、OpenAI、通义、智谱等）。实践中遇到：

1. 每个 provider 的 key、base_url、调用方式分散在各项目里，改一处要动多处。
2. 每次调用花了多少钱、用了多少 token，不透明。
3. 重试、超时、限流、故障切换，每个项目都要重写一遍。
4. 相同或相似的问题反复问，重复付费。

因此需要一个**统一入口**：所有 AI 项目都指向它，既能转发到任意 provider，又能看到成本、控制花费。

### 1.2 目标

1. 提供 OpenAI 兼容的统一 API，支持多 provider 转发，含 SSE 流式输出。
2. 主 provider 失败时自动切换到备用 provider（故障转移）。
3. 每次调用的 token / 成本落库，可查询历史用量。
4. 预算护栏：用量接近上限时降级到便宜模型，超上限时拒绝。
5. 降本缓存：精确缓存 + 语义缓存。
6. 可观测：Prometheus `/metrics` + 极简用量看板页。

### 1.3 非目标（本期明确不做）

- OpenTelemetry 链路追踪 / 请求回放
- 多租户、用户系统、计费后台（本项目仅单用户）
- 模型微调、Agent 编排、RAG 检索
- 分布式部署 / 水平扩展 / 高可用集群

> 这些留作"后期扩展方向"，简历中一句话带过，不投入本期工时。

---

## 2. 技术栈

| 用途 | 选型 | 说明 |
|---|---|---|
| 语言 | Go 1.22+ | |
| HTTP 框架 | **Gin** | 唯一的 Web 框架；SSE 用 `c.Stream` |
| 上游转发 | 标准库 `net/http` | 作为反向代理直接转发，**不引任何 LLM SDK** |
| 流式 | Gin `Stream` / `http.Flusher` | 无需额外库 |
| 存储 | `modernc.org/sqlite`（纯 Go，无 CGO） | 调用日志 / 成本 / 预算 / 缓存条目 |
| 指标 | `prometheus/client_golang` | 暴露 `/metrics` |
| 日志 | 标准库 `log/slog` | 结构化日志（Go 1.21+ 自带） |
| 配置 | `godotenv` + `os.Getenv` + YAML | 简单够用 |
| 看板 | Go `embed` 内嵌静态 HTML + Chart.js | 极简用量曲线 |
| 测试 | 标准库 `testing` | |
| 部署 | Docker + docker-compose | 单服务 |

**说明：** 除 Gin 外无重框架；不引入 LangChain / OpenAI SDK / ORM / 缓存中间件（Redis 本期不引入，缓存先落 SQLite + 内存）。

---

## 3. 系统架构

### 3.1 分层（普通分层，非六边形仪式感）

```
[上游: AI 应用 / OpenAI SDK / curl]
        │  OpenAI 兼容 REST + SSE
┌───────▼──────────────────────────────────────┐
│  API 层 (Gin)                                   │
│   POST /v1/chat/completions  (SSE)              │
│   POST /v1/embeddings                           │
│   GET  /v1/models                               │
│   GET  /admin/usage   GET /metrics   /healthz   │
│   中间件: 鉴权(固定token) / RequestID / recover  │
├───────────────────────────────────────────────┤
│  业务层                                         │
│   ProxyService   编排一次请求全流程              │
│   CacheService   精确 + 语义缓存                 │
│   BudgetService  预算规则(放行/降级/拒绝)        │
│   BillingService 计量 + 落库                     │
├───────────────────────────────────────────────┤
│  领域逻辑 (纯函数, 可单测)                       │
│   Router     选主 provider + 备用顺序            │
│   Pricing    模型 → 单价                         │
│   Meter      token 计数 + 成本计算               │
│   BudgetRule "超预算怎么办"决策                  │
│   Similarity 余弦相似度判定                      │
├───────────────────────────────────────────────┤
│  基础设施                                       │
│   Provider(DeepSeek/OpenAI/通义/智谱)            │
│   UsageRepo(SQLite)  Metrics(Prometheus)  Logger │
└───────────────────────────────────────────────┘
```

**可替换边界：** `Provider`、`UsageRepo`、`Metrics` 均定义接口，便于注入 fake 做单测。

### 3.2 目录结构

```
llm-gateway/
  cmd/server/main.go        # 装配依赖 + 启动 HTTP
  internal/
    api/                    # Gin handler + 中间件 + DTO
    proxy/                  # 转发编排 + 故障转移
    provider/               # 各 provider 适配 (接口 + 实现)
    cache/                  # 精确缓存 + 语义缓存
    budget/                 # 预算规则
    usage/                  # SQLite 记录 + 查询
    metrics/                # Prometheus 指标
    config/                 # 配置加载
  web/                      # 内嵌极简看板页 (embed)
  configs/                  # 配置样例 (config.yaml / .env.example)
  docs/superpowers/specs/   # 本文档
  test/                     # 集成测试 + testdata
  Dockerfile
  docker-compose.yml
  go.mod
```

---

## 4. 数据流

一次 `POST /v1/chat/completions` 的完整路径：

```
  1. 鉴权        校验网关固定 token（Authorization）
  2. request_id  生成 ID + 开始计时
  3. 精确缓存    request hash(model+messages+params) 命中 → 直接返回
  4. 语义缓存    取末条 user 文本求 embedding → 与缓存集合算余弦 ≥ 阈值 → 命中返回
  5. 预算校验    查当月累计花费:
                  正常      → 放行原模型
                  接近上限  → 降级到便宜模型
                  超上限    → 拒绝 (429 + 错误体)
  6. 路由        选主 provider
  7. 调用        失败 → 指数退避重试 → 切备用 provider → 都失败返回 502
  8. 输出        流式逐 token 透传 SSE
  9. 收尾        token + 成本 落库 / 写回缓存(精确+语义) / 更新 Prometheus
```

**关键决策：**
- **缓存先于预算**：命中缓存不产生费用，因此不应触发预算判断。
- **仅完整回答写缓存**：流式请求也先查缓存，命中则一次性返回完整结果。
- **语义缓存失败可降级**：embedding 调用失败时跳过语义缓存，继续正常流程。

---

## 5. 功能点与优先级

| 优先级 | 功能 | 说明 |
|---|---|---|
| **MVP** | OpenAI 兼容接口 | `/v1/chat/completions`、`/v1/embeddings`、`/v1/models` |
| **MVP** | 多 provider 转发 | 至少 DeepSeek + 1 个备用（OpenAI/通义） |
| **MVP** | SSE 流式 | 逐 token 透传 |
| **MVP** | 故障转移 | 主失败重试后切备用 |
| **MVP** | 成本落库 | token/成本写 SQLite + `/admin/usage` 查询 |
| **MVP** | 固定 token 鉴权 | 防止网关裸奔 |
| **MVP** | Docker 一键起 | + 测试 + README |
| **MVP** | 精确缓存 | 请求 hash 命中直接返回 |
| **P2** | 语义缓存 | 余弦相似度，≥ 阈值命中 |
| **P2** | 预算护栏 | 月度聚合 + 放行/降级/拒绝规则 |
| **P2** | Prometheus | `/metrics` 暴露请求数/延迟/token/成本 |
| **P2** | 极简用量看板 | 内嵌 HTML + Chart.js 画用量/成本曲线 |
| 后期 | OTel、多租户、请求回放 | 明确不做 |

---

## 6. 错误处理

总原则：**能降级就降级，辅助功能失败不中断主流程。**

| 场景 | 处理 |
|---|---|
| provider 失败 | 指数退避重试 N 次 → 切备用 → 全失败返回 `502` |
| 语义缓存 embedding 失败 | 跳过语义缓存，继续正常请求 |
| 缓存 / SQLite 不可用 | 跳过缓存直连，不阻塞 |
| 预算超上限 | `429` + 明确错误体 |
| 鉴权失败 | `401` |
| 请求非法 | `400` |
| 上游超时 | context 超时 → `504` |
| panic | recover 中间件 → `500`，不崩溃 |

---

## 7. 测试策略

- **单元测试**：
  - 预算规则（放行 / 降级 / 拒绝的边界值）
  - 成本计算（Pricing 表 + token 计数）
  - 缓存匹配（精确 hash、语义阈值）
  - 路由与故障转移顺序
- **集成测试**：fake `Provider`（返回固定流）跑通全链路；SQLite 用临时/内存库。
- **手动验收**：真实 key 打一次流式；确认 usage 落库、`/metrics` 有数、看板能出图。
- 重点覆盖领域逻辑，基础设施用 fake / 契约测试。

---

## 8. 里程碑

| 阶段 | 产出 | 验收 |
|---|---|---|
| P0 | 脚手架、配置、`/healthz`、鉴权中间件 | 服务能起，`/healthz` 通 |
| P1 | 单 provider 转发 + SSE 流式 | 用真实 key 能流式问答 |
| P2 | 多 provider + 故障转移 + 成本落库 | 主挂自动切备用；usage 有记录 |
| P3 | 精确缓存 + 预算护栏 | 命中缓存不计费；超限被拒 |
| P4 | 语义缓存 + Prometheus + 用量看板 | 看板出曲线；`/metrics` 有数 |
| P5 | Docker 一键起 + README + 测试补全 | `docker compose up` 可跑 |

---

## 9. 关键取舍

- **只做单用户**：省掉鉴权体系/多租户/计费后台，把精力集中在网关核心与成本护栏。
- **不引 Redis**：单用户场景 SQLite + 内存足够，减少部署依赖（后期量大再引）。
- **反向代理而非 SDK**：直接转发原始 HTTP，兼容任意 OpenAI 兼容客户端，且实现干净。
- **自研而非用现成网关**（one-api / LiteLLM）：本项目为简历与学习目的，且聚焦"个人成本护栏"这一差异点。

---

## 附：命名与文档

- 代码块归属标记：本仓库所有实现属于项目 `llm-gateway`。
- 已归档项目：`rag-kb`（代码保留，不自动加载，不参与本项目）。