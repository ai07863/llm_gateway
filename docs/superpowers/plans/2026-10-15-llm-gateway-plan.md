# LLM 统一网关（llm-gateway）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个单用户、OpenAI 兼容的 LLM 统一网关，支持多 provider 转发、SSE 流式、故障转移、成本落库、缓存与预算护栏。

**Architecture:** 普通分层（API / 业务 / 领域 / 基础设施）。上游以反向代理方式原样转发 OpenAI 兼容 HTTP，不引入任何 LLM SDK。领域逻辑为纯函数便于单测，基础设施（Provider / UsageRepo / Metrics）定义接口便于注入 fake。

**Tech Stack:** Go 1.22+、Gin、标准库 net/http（上游转发）、modernc.org/sqlite、prometheus/client_golang、log/slog、godotenv、go:embed。

## Global Constraints

- Go 版本下限：`go 1.22`
- 模块名：`llm-gateway`
- 单用户：不做多租户 / 用户系统 / 计费后台
- 不引入 Redis；缓存使用内存 + SQLite
- 不引入任何 LLM SDK / LangChain / ORM；上游用标准库 `net/http` 转发
- 上游 Provider 必须 OpenAI 兼容（`base_url` + `/chat/completions`、`/embeddings`）
- 配置来源：环境变量优先，`configs/config.yaml` 兜底
- 每个任务以 TDD 循环推进，结束时必须提交（commit）
- 所有密钥（API key）只从环境变量读取，禁止硬编码

---

## 文件结构

```
llm-gateway/
  go.mod
  cmd/server/main.go              # 装配依赖 + 启动 HTTP
  internal/
    config/config.go              # 配置加载（env + yaml）
    api/router.go                 # Gin 路由注册
    api/middleware.go             # 鉴权 / RequestID / recover
    api/chat.go                   # /v1/chat/completions
    api/embeddings.go             # /v1/embeddings
    api/models.go                 # /v1/models
    api/admin.go                  # /admin/usage
    provider/provider.go          # Provider 接口 + 公共类型
    provider/openai_compat.go     # OpenAI 兼容转发实现（含流式）
    proxy/proxy.go                # 编排：缓存→预算→路由→转发→落库
    proxy/failover.go             # 故障转移/重试
    cache/exact.go                # 精确缓存（内存 TTL）
    cache/semantic.go             # 语义缓存
    budget/budget.go              # 预算规则（纯函数）
    usage/usage.go                # Record 类型 + Repo 接口
    usage/sqlite.go               # SQLite 实现
    metrics/metrics.go            # Prometheus 指标
    pricing/pricing.go            # 模型单价 + 成本计算
    similarity/similarity.go      # 余弦相似度
  web/index.html                  # 内嵌用量看板
  configs/config.yaml
  configs/.env.example
  tests/integration_test.go
  Dockerfile
  docker-compose.yml
  README.md
```

---

### Task 1: 脚手架 + 配置 + 健康检查 + 鉴权中间件

**Files:**
- Create: `llm-gateway/go.mod`
- Create: `llm-gateway/internal/config/config.go`
- Create: `llm-gateway/internal/api/middleware.go`
- Create: `llm-gateway/internal/api/router.go`
- Create: `llm-gateway/cmd/server/main.go`
- Create: `llm-gateway/configs/.env.example`
- Test: `llm-gateway/internal/api/middleware_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `config.Settings` struct，字段 `GatewayToken string`、`ListenAddr string`、`Providers []config.ProviderConfig`、`DBPath string`、`BudgetMonthlyLimit float64`、`BudgetSoftRatio float64`
  - `config.Load() (Settings, error)`
  - `api.NewRouter(s config.Settings) *gin.Engine`
  - `api.AuthMiddleware(token string) gin.HandlerFunc`

- [ ] **Step 1: 初始化模块**

```bash
cd llm-gateway
go mod init llm-gateway
go get github.com/gin-gonic/gin@latest
```

- [ ] **Step 2: 写失败测试**

```go
// internal/api/middleware_test.go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", AuthMiddleware("secret"), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuthMiddlewareAcceptsValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", AuthMiddleware("secret"), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/api/ -run TestAuthMiddleware -v`
Expected: FAIL（`AuthMiddleware` 未定义）

- [ ] **Step 4: 实现配置**

```go
// internal/config/config.go
package config

import (
	"os"
	"strconv"
)

type ProviderConfig struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

type Settings struct {
	GatewayToken       string
	ListenAddr         string
	DBPath             string
	BudgetMonthlyLimit float64
	BudgetSoftRatio    float64
	Providers          []ProviderConfig
}

func Load() (Settings, error) {
	s := Settings{
		GatewayToken:       os.Getenv("GATEWAY_TOKEN"),
		ListenAddr:         getenv("LISTEN_ADDR", ":8080"),
		DBPath:             getenv("DB_PATH", "gateway.db"),
		BudgetMonthlyLimit: getfloat("BUDGET_MONTHLY_LIMIT", 20),
		BudgetSoftRatio:    getfloat("BUDGET_SOFT_RATIO", 0.8),
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "deepseek", BaseURL: getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			APIKey: key, Model: getenv("DEEPSEEK_MODEL", "deepseek-chat"),
		})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "openai", BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey: key, Model: getenv("OPENAI_MODEL", "gpt-4o-mini"),
		})
	}
	return s, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getfloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
```

- [ ] **Step 5: 实现中间件**

```go
// internal/api/middleware.go
package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if token == "" || auth != "Bearer "+token {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": gin.H{"message": "invalid or missing token", "type": "auth_error"},
			})
			return
		}
		c.Next()
	}
}

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func strings_HasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
```

- [ ] **Step 6: 实现 requestID 辅助**

```go
// internal/api/requestid.go
package api

import (
	"crypto/rand"
	"encoding/hex"
)

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}
```

- [ ] **Step 7: 实现路由与入口**

```go
// internal/api/router.go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func NewRouter(s config.Settings) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), RequestIDMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	v1 := r.Group("/v1", AuthMiddleware(s.GatewayToken))
	_ = v1 // 后续任务注册 chat/embeddings/models
	return r
}
```

```go
// cmd/server/main.go
package main

import (
	"log"

	"llm-gateway/internal/api"
	"llm-gateway/internal/config"
)

func main() {
	s, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	r := api.NewRouter(s)
	log.Printf("listening on %s", s.ListenAddr)
	if err := r.Run(s.ListenAddr); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 8: 写配置样例**

```
# configs/.env.example
GATEWAY_TOKEN=change-me
LISTEN_ADDR=:8080
DB_PATH=gateway.db
BUDGET_MONTHLY_LIMIT=20
BUDGET_SOFT_RATIO=0.8
DEEPSEEK_API_KEY=
DEEPSEEK_BASE_URL=https://api.deepseek.com/v1
DEEPSEEK_MODEL=deepseek-chat
OPENAI_API_KEY=
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
```

- [ ] **Step 9: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 10: 提交**

```bash
git add -A
git commit -m "feat: 脚手架、配置、健康检查与鉴权中间件"
```

---

### Task 2: Provider 抽象 + OpenAI 兼容非流式转发

**Files:**
- Create: `llm-gateway/internal/provider/provider.go`
- Create: `llm-gateway/internal/provider/openai_compat.go`
- Create: `llm-gateway/internal/api/models.go`
- Create: `llm-gateway/internal/api/chat.go`
- Test: `llm-gateway/internal/provider/openai_compat_test.go`

**Interfaces:**
- Consumes: `config.ProviderConfig`
- Produces:
  - `provider.Message{Role, Content string}`
  - `provider.ChatRequest{Model string; Messages []Message; Temperature *float64}`
  - `provider.ChatResponse{Model, Content string; PromptTokens, CompletionTokens, TotalTokens int}`
  - `provider.Provider` interface：`Name() string`、`Chat(ctx, ChatRequest) (ChatResponse, error)`
  - `provider.NewOpenAICompat(cfg config.ProviderConfig) Provider`

- [ ] **Step 1: 写失败测试（用 httptest 假上游）**

```go
// internal/provider/openai_compat_test.go
package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"llm-gateway/internal/config"
)

func TestOpenAICompatChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"deepseek-chat",
			"choices":[{"message":{"role":"assistant","content":"hi there"}}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "deepseek-chat"})
	resp, err := p.Chat(context.Background(), ChatRequest{
		Model:    "deepseek-chat",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("chat error: %v", err)
	}
	if resp.Content != "hi there" {
		t.Fatalf("want 'hi there', got %q", resp.Content)
	}
	if resp.TotalTokens != 5 {
		t.Fatalf("want 5 tokens, got %d", resp.TotalTokens)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/provider/ -v`
Expected: FAIL（类型未定义）

- [ ] **Step 3: 实现类型与接口**

```go
// internal/provider/provider.go
package provider

import "context"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
}

type ChatResponse struct {
	Model            string `json:"model"`
	Content          string `json:"content"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
```

- [ ] **Step 4: 实现 OpenAI 兼容转发**

```go
// internal/provider/openai_compat.go
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"llm-gateway/internal/config"
)

type openAICompat struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func NewOpenAICompat(cfg config.ProviderConfig) Provider {
	return &openAICompat{cfg: cfg, client: &http.Client{}}
}

func (p *openAICompat) Name() string { return p.cfg.Name }

type oaChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (p *openAICompat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ChatResponse{}, fmt.Errorf("provider %s status %d: %s", p.cfg.Name, res.StatusCode, string(b))
	}
	var parsed oaChatResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return ChatResponse{}, err
	}
	out := ChatResponse{
		Model:            parsed.Model,
		TotalTokens:      parsed.Usage.TotalTokens,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
	}
	return out, nil
}
```

- [ ] **Step 5: 实现 models 与 chat handler（非流式）**

```go
// internal/api/models.go
package api

import "github.com/gin-gonic/gin"

func registerModels(v1 *gin.RouterGroup, models []string) {
	v1.GET("/models", func(c *gin.Context) {
		data := make([]gin.H, 0, len(models))
		for _, m := range models {
			data = append(data, gin.H{"id": m, "object": "model"})
		}
		c.JSON(200, gin.H{"object": "list", "data": data})
	})
}
```

```go
// internal/api/chat.go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type chatBody struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature *float64           `json:"temperature"`
}

func registerChat(v1 *gin.RouterGroup, chat func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse) {
	v1.POST("/chat/completions", func(c *gin.Context) {
		var body chatBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		resp := chat(c, provider.ChatRequest{Model: body.Model, Messages: body.Messages, Temperature: body.Temperature})
		c.JSON(http.StatusOK, gin.H{
			"model": resp.Model,
			"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": resp.Content}}},
			"usage": gin.H{
				"prompt_tokens": resp.PromptTokens, "completion_tokens": resp.CompletionTokens, "total_tokens": resp.TotalTokens,
			},
		})
	})
}
```

- [ ] **Step 6: 在 router 中接线（临时用第一个 provider）**

在 `internal/api/router.go` 的 `NewRouter` 中，构造 provider 列表并注册：

```go
	providers := make([]provider.Provider, 0, len(s.Providers))
	models := make([]string, 0, len(s.Providers))
	for _, pc := range s.Providers {
		providers = append(providers, provider.NewOpenAICompat(pc))
		models = append(models, pc.Model)
	}
	registerModels(v1, models)
	registerChat(v1, func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse {
		if len(providers) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "no provider configured"}})
			return provider.ChatResponse{}
		}
		resp, err := providers[0].Chat(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return provider.ChatResponse{}
		}
		return resp
	})
```

补充 import：`"net/http"`、`"llm-gateway/internal/provider"`。

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "feat: Provider 抽象与 OpenAI 兼容非流式转发"
```

---

### Task 3: SSE 流式透传

**Files:**
- Modify: `llm-gateway/internal/provider/provider.go`（新增 `StreamChunk`、`ChatStream`）
- Modify: `llm-gateway/internal/provider/openai_compat.go`（实现 `ChatStream`）
- Modify: `llm-gateway/internal/api/chat.go`（`stream:true` 分支）
- Test: `llm-gateway/internal/provider/openai_compat_stream_test.go`

**Interfaces:**
- Consumes: `Provider`、`ChatRequest`
- Produces:
  - `provider.StreamChunk{Content string; Done bool}`
  - `Provider.ChatStream(ctx, ChatRequest) (<-chan StreamChunk, error)`

- [ ] **Step 1: 写失败测试（假 SSE 上游）**

```go
// internal/provider/openai_compat_stream_test.go
package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-gateway/internal/config"
)

func TestOpenAICompatChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, tok := range []string{"Hel", "lo", "!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tok)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	var sb strings.Builder
	for c := range ch {
		if c.Done {
			break
		}
		sb.WriteString(c.Content)
	}
	if sb.String() != "Hello!" {
		t.Fatalf("want 'Hello!', got %q", sb.String())
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/provider/ -run TestOpenAICompatChatStream -v`
Expected: FAIL

- [ ] **Step 3: 扩展接口类型**

在 `internal/provider/provider.go` 增加：

```go
type StreamChunk struct {
	Content string
	Done    bool
}
```

并在 `Provider` 接口中增加：

```go
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
```

- [ ] **Step 4: 实现流式解析**

在 `internal/provider/openai_compat.go` 增加：

```go
import (
	"bufio"
	"strings"
)

func (p *openAICompat) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	res, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, fmt.Errorf("provider %s status %d: %s", p.cfg.Name, res.StatusCode, string(b))
	}

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}
			var parsed struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
				continue
			}
			if len(parsed.Choices) > 0 && parsed.Choices[0].Delta.Content != "" {
				ch <- StreamChunk{Content: parsed.Choices[0].Delta.Content}
			}
		}
	}()
	return ch, nil
}
```

注意：`ChatRequest` 需要新增 `Stream bool json:"stream,omitempty"` 字段。

- [ ] **Step 5: chat handler 支持 stream**

将 `internal/api/chat.go` 的 `registerChat` 改为接收 `chatStream` 回调并在 `body.Stream` 为真时使用：

```go
type chatBody struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature *float64           `json:"temperature"`
	Stream      bool               `json:"stream"`
}

func registerChat(
	v1 *gin.RouterGroup,
	chat func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse,
	chatStream func(c *gin.Context, req provider.ChatRequest),
) {
	v1.POST("/chat/completions", func(c *gin.Context) {
		var body chatBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		req := provider.ChatRequest{Model: body.Model, Messages: body.Messages, Temperature: body.Temperature}
		if body.Stream {
			chatStream(c, req)
			return
		}
		resp := chat(c, req)
		c.JSON(http.StatusOK, gin.H{
			"model":   resp.Model,
			"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": resp.Content}}},
			"usage":   gin.H{"prompt_tokens": resp.PromptTokens, "completion_tokens": resp.CompletionTokens, "total_tokens": resp.TotalTokens},
		})
	})
}
```

- [ ] **Step 6: 在 router 中接线流式（临时用第一个 provider）**

```go
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse {
			if len(providers) == 0 {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "no provider configured"}})
				return provider.ChatResponse{}
			}
			resp, err := providers[0].Chat(c.Request.Context(), req)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
				return provider.ChatResponse{}
			}
			return resp
		},
		func(c *gin.Context, req provider.ChatRequest) {
			if len(providers) == 0 {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "no provider configured"}})
				return
			}
			ch, err := providers[0].ChatStream(c.Request.Context(), req)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
				return
			}
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Stream(func(w io.Writer) bool {
				chunk, ok := <-ch
				if !ok || chunk.Done {
					fmt.Fprint(w, "data: [DONE]\n\n")
					return false
				}
				fmt.Fprintf(w, "data: %s\n\n", mustJSON(gin.H{"choices": []gin.H{{"delta": gin.H{"content": chunk.Content}}}}))
				return true
			})
		},
	)
```

在 `internal/api/chat.go` 增加辅助：

```go
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
```

补充 imports：`"encoding/json"`、`"fmt"`、`"io"`。

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "feat: SSE 流式透传"
```

---

### Task 4: 多 provider 故障转移 + 重试

**Files:**
- Create: `llm-gateway/internal/proxy/failover.go`
- Test: `llm-gateway/internal/proxy/failover_test.go`
- Modify: `llm-gateway/internal/api/router.go`

**Interfaces:**
- Consumes: `provider.Provider`、`provider.ChatRequest`
- Produces:
  - `proxy.ChatWithFailover(ctx, providers []provider.Provider, req ChatRequest) (ChatResponse, provider.Provider, error)`
  - `proxy.StreamWithFailover(ctx, providers []provider.Provider, req ChatRequest) (<-chan StreamChunk, provider.Provider, error)`

- [ ] **Step 1: 写失败测试（第一个 provider 失败，第二个成功）**

```go
// internal/proxy/failover_test.go
package proxy

import (
	"context"
	"errors"
	"testing"

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

func TestChatWithFailoverUsesBackup(t *testing.T) {
	ps := []provider.Provider{
		fakeProvider{name: "primary", fail: true},
		fakeProvider{name: "backup", content: "ok"},
	}
	resp, used, err := ChatWithFailover(context.Background(), ps, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if resp.Content != "ok" || used.Name() != "backup" {
		t.Fatalf("want backup/ok, got %s/%s", used.Name(), resp.Content)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/proxy/ -v`
Expected: FAIL

- [ ] **Step 3: 实现故障转移**

```go
// internal/proxy/failover.go
package proxy

import (
	"context"

	"llm-gateway/internal/provider"
)

func ChatWithFailover(ctx context.Context, providers []provider.Provider, req provider.ChatRequest) (provider.ChatResponse, provider.Provider, error) {
	var lastErr error
	for _, p := range providers {
		resp, err := p.Chat(ctx, req)
		if err == nil {
			return resp, p, nil
		}
		lastErr = err
	}
	return provider.ChatResponse{}, nil, lastErr
}

func StreamWithFailover(ctx context.Context, providers []provider.Provider, req provider.ChatRequest) (<-chan provider.StreamChunk, provider.Provider, error) {
	var lastErr error
	for _, p := range providers {
		ch, err := p.ChatStream(ctx, req)
		if err == nil {
			return ch, p, nil
		}
		lastErr = err
	}
	return nil, nil, lastErr
}
```

- [ ] **Step 4: router 改为使用故障转移**

将 Task 2/3 中"直接用 `providers[0]`"的临时回调替换为：

```go
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse {
			resp, _, err := proxy.ChatWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
				return provider.ChatResponse{}
			}
			return resp
		},
		func(c *gin.Context, req provider.ChatRequest) {
			ch, _, err := proxy.StreamWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
				return
			}
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Stream(func(w io.Writer) bool {
				chunk, ok := <-ch
				if !ok || chunk.Done {
					fmt.Fprint(w, "data: [DONE]\n\n")
					return false
				}
				fmt.Fprintf(w, "data: %s\n\n", mustJSON(gin.H{"choices": []gin.H{{"delta": gin.H{"content": chunk.Content}}}}))
				return true
			})
		},
	)
```

注意：失败 provider 应在列表前部优先，成功后可对成功者做有序提升（本任务不做，P2 可加）。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: 多 provider 故障转移"
```

---

### Task 5: 成本/Token 落库 + /admin/usage

**Files:**
- Create: `llm-gateway/internal/pricing/pricing.go`
- Create: `llm-gateway/internal/usage/usage.go`
- Create: `llm-gateway/internal/usage/sqlite.go`
- Create: `llm-gateway/internal/api/admin.go`
- Test: `llm-gateway/internal/pricing/pricing_test.go`
- Test: `llm-gateway/internal/usage/sqlite_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `pricing.Cost(model string, promptTokens, completionTokens int) float64`
  - `usage.Record{ID, Model, Provider string; PromptTokens, CompletionTokens int; Cost float64; CacheHit bool; CreatedAt time.Time}`
  - `usage.Repo` 接口：`Record(ctx, Record) error`、`MonthlyCost(ctx, yyyymm string) (float64, error)`、`List(ctx, limit int) ([]Record, error)`
  - `usage.NewSQLite(path string) (Repo, error)`
  - `api.registerAdmin(v1 *gin.RouterGroup, repo usage.Repo)`

- [ ] **Step 1: 写 pricing 失败测试**

```go
// internal/pricing/pricing_test.go
package pricing

import "testing"

func TestCostKnownModel(t *testing.T) {
	// deepseek-chat: 输入 0.27 元/百万 token，输出 1.10 元/百万 token（示例价）
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
```

- [ ] **Step 2: 实现 pricing**

```go
// internal/pricing/pricing.go
package pricing

type rate struct {
	in, out float64 // 元 / 百万 token
}

var table = map[string]rate{
	"deepseek-chat": {0.27, 1.10},
	"gpt-4o-mini":   {1.10, 4.40},
}

func Cost(model string, promptTokens, completionTokens int) float64 {
	r, ok := table[model]
	if !ok {
		return 0
	}
	return float64(promptTokens)/1e6*r.in + float64(completionTokens)/1e6*r.out
}
```

- [ ] **Step 3: 写 usage 失败测试**

```go
// internal/usage/sqlite_test.go
package usage

import (
	"context"
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
```

- [ ] **Step 4: 实现 usage 类型与 SQLite**

```go
// internal/usage/usage.go
package usage

import (
	"context"
	"time"
)

type Record struct {
	ID               string
	Model            string
	Provider         string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	CacheHit         bool
	CreatedAt        time.Time
}

type Repo interface {
	Record(ctx context.Context, r Record) error
	MonthlyCost(ctx context.Context, yyyymm string) (float64, error)
	List(ctx context.Context, limit int) ([]Record, error)
	Close() error
}

func CurrentMonth() string { return time.Now().Format("2006-01") }
```

```go
// internal/usage/sqlite.go
package usage

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

type sqliteRepo struct{ db *sql.DB }

func NewSQLite(path string) (Repo, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS usage_records (
		id TEXT PRIMARY KEY,
		model TEXT, provider TEXT,
		prompt_tokens INTEGER, completion_tokens INTEGER,
		cost REAL, cache_hit INTEGER, created_at TEXT)`)
	if err != nil {
		return nil, err
	}
	return &sqliteRepo{db: db}, nil
}

func (r *sqliteRepo) Record(ctx context.Context, rec Record) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = NowFunc()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO usage_records (id,model,provider,prompt_tokens,completion_tokens,cost,cache_hit,created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Model, rec.Provider, rec.PromptTokens, rec.CompletionTokens, rec.Cost, boolToInt(rec.CacheHit), rec.CreatedAt.Format("2006-01-02 15:04:05"))
	return err
}

func (r *sqliteRepo) MonthlyCost(ctx context.Context, yyyymm string) (float64, error) {
	var total sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		`SELECT SUM(cost) FROM usage_records WHERE substr(created_at,1,7) = ?`, yyyymm).Scan(&total)
	return total.Float64, err
}

func (r *sqliteRepo) List(ctx context.Context, limit int) ([]Record, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id,model,provider,prompt_tokens,completion_tokens,cost,cache_hit,created_at
		 FROM usage_records ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var rec Record
		var hit int
		var ts string
		if err := rows.Scan(&rec.ID, &rec.Model, &rec.Provider, &rec.PromptTokens, &rec.CompletionTokens, &rec.Cost, &hit, &ts); err != nil {
			return nil, err
		}
		rec.CacheHit = hit == 1
		out = append(out, rec)
	}
	return out, nil
}

func (r *sqliteRepo) Close() error { return r.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

需要新增依赖与辅助：`go get modernc.org/sqlite@latest`；并在 `internal/usage/usage.go` 增加 `var NowFunc = time.Now` 供测试替换。

- [ ] **Step 5: 实现 admin handler**

```go
// internal/api/admin.go
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/usage"
)

func registerAdmin(v1 *gin.RouterGroup, repo usage.Repo) {
	v1.GET("/admin/usage", func(c *gin.Context) {
		records, err := repo.List(c.Request.Context(), 100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		month, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
		c.JSON(http.StatusOK, gin.H{"month_cost": month, "records": records})
	})
}
```

- [ ] **Step 6: 落库接线（在 chat 成功后记录）**

在 `internal/api/router.go` 构造 `usage.NewSQLite(s.DBPath)`，并在 chat 回调成功后写入：

```go
	repo, err := usage.NewSQLite(s.DBPath)
	if err != nil {
		panic(err)
	}
	registerAdmin(v1, repo)

	recordUsage := func(resp provider.ChatResponse, p provider.Provider) {
		_ = repo.Record(context.Background(), usage.Record{
			ID:               newRequestID(),
			Model:            resp.Model,
			Provider:         p.Name(),
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
			Cost:             pricing.Cost(resp.Model, resp.PromptTokens, resp.CompletionTokens),
		})
	}
```

在非流式分支：`resp, used, err := proxy.ChatWithFailover(...)`；成功后 `recordUsage(resp, used)`。

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "feat: 成本/Token 落库与用量查询接口"
```

---

### Task 6: 精确缓存

**Files:**
- Create: `llm-gateway/internal/cache/exact.go`
- Modify: `llm-gateway/internal/api/router.go`
- Test: `llm-gateway/internal/cache/exact_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `cache.Exact` 类型：`Get(key string) (string, bool)`、`Set(key string, value string)`、`Key(req provider.ChatRequest) string`
  - `cache.NewExact(ttl time.Duration) *Exact`

- [ ] **Step 1: 写失败测试**

```go
// internal/cache/exact_test.go
package cache

import (
	"testing"
	"time"

	"llm-gateway/internal/provider"
)

func TestExactCacheHitAndMiss(t *testing.T) {
	c := NewExact(time.Minute)
	req := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	key := c.Key(req)

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss")
	}
	c.Set(key, "answer")
	if v, ok := c.Get(key); !ok || v != "answer" {
		t.Fatalf("expected hit 'answer', got %q %v", v, ok)
	}
}

func TestExactCacheExpiry(t *testing.T) {
	now := time.Now()
	NowFunc = func() time.Time { return now }
	defer func() { NowFunc = time.Now }()

	c := NewExact(time.Second)
	key := "k"
	c.Set(key, "v")
	now = now.Add(2 * time.Second)
	if _, ok := c.Get(key); ok {
		t.Fatal("expected expired")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/cache/ -v`
Expected: FAIL

- [ ] **Step 3: 实现精确缓存**

```go
// internal/cache/exact.go
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"llm-gateway/internal/provider"
)

var NowFunc = time.Now

type entry struct {
	value   string
	expires time.Time
}

type Exact struct {
	mu  sync.RWMutex
	ttl time.Duration
	m   map[string]entry
}

func NewExact(ttl time.Duration) *Exact {
	return &Exact{ttl: ttl, m: make(map[string]entry)}
}

func (c *Exact) Key(req provider.ChatRequest) string {
	b, _ := json.Marshal(struct {
		Model    string
		Messages []provider.Message
	}{req.Model, req.Messages})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (c *Exact) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[key]
	if !ok || NowFunc().After(e.expires) {
		return "", false
	}
	return e.value, true
}

func (c *Exact) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = entry{value: value, expires: NowFunc().Add(c.ttl)}
}
```

- [ ] **Step 4: 接入 router（非流式命中缓存）**

在 router 构造 `exact := cache.NewExact(10 * time.Minute)`，非流式回调改为：

```go
		func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse {
			key := exact.Key(req)
			if cached, ok := exact.Get(key); ok {
				return provider.ChatResponse{Model: req.Model, Content: cached}
			}
			resp, used, err := proxy.ChatWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
				return provider.ChatResponse{}
			}
			exact.Set(key, resp.Content)
			recordUsage(resp, used)
			return resp
		},
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: 精确缓存"
```

---

### Task 7: 预算护栏

**Files:**
- Create: `llm-gateway/internal/budget/budget.go`
- Modify: `llm-gateway/internal/api/router.go`
- Test: `llm-gateway/internal/budget/budget_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `budget.Decision`（`Allow` / `Degrade` / `Reject`）
  - `budget.Evaluate(monthlyCost, limit, softRatio float64) Decision`
  - `budget.CheaperModel(model string) string`

- [ ] **Step 1: 写失败测试**

```go
// internal/budget/budget_test.go
package budget

import "testing"

func TestEvaluate(t *testing.T) {
	// limit=20, softRatio=0.8 → soft 阈值 16
	cases := []struct {
		cost float64
		want Decision
	}{
		{5, Allow},
		{15.99, Allow},
		{16, Degrade},
		{19.99, Degrade},
		{20, Reject},
		{25, Reject},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.cost, 20, 0.8); got != tc.want {
			t.Fatalf("cost=%v want %v got %v", tc.cost, tc.want, got)
		}
	}
}

func TestCheaperModel(t *testing.T) {
	if CheaperModel("gpt-4o-mini") != "deepseek-chat" {
		t.Fatal("gpt-4o-mini should degrade to deepseek-chat")
	}
	if CheaperModel("deepseek-chat") != "deepseek-chat" {
		t.Fatal("already cheapest stays same")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/budget/ -v`
Expected: FAIL

- [ ] **Step 3: 实现预算规则**

```go
// internal/budget/budget.go
package budget

type Decision int

const (
	Allow Decision = iota
	Degrade
	Reject
)

func Evaluate(monthlyCost, limit, softRatio float64) Decision {
	if limit <= 0 {
		return Allow
	}
	soft := limit * softRatio
	switch {
	case monthlyCost >= limit:
		return Reject
	case monthlyCost >= soft:
		return Degrade
	default:
		return Allow
	}
}

var cheaper = map[string]string{
	"gpt-4o":      "deepseek-chat",
	"gpt-4o-mini": "deepseek-chat",
	"deepseek-chat": "deepseek-chat",
}

func CheaperModel(model string) string {
	if m, ok := cheaper[model]; ok {
		return m
	}
	return model
}
```

- [ ] **Step 4: 接入 router（缓存后、转发前）**

非流式回调在缓存未命中后插入预算判断：

```go
			cost, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
			switch budget.Evaluate(cost, s.BudgetMonthlyLimit, s.BudgetSoftRatio) {
			case budget.Reject:
				c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"message": "monthly budget exceeded"}})
				return provider.ChatResponse{}
			case budget.Degrade:
				req.Model = budget.CheaperModel(req.Model)
			}
```

（预算判断放在缓存命中之后，符合 spec「缓存先于预算」。）

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: 预算护栏（放行/降级/拒绝）"
```

---

### Task 8: 语义缓存

**Files:**
- Create: `llm-gateway/internal/similarity/similarity.go`
- Create: `llm-gateway/internal/cache/semantic.go`
- Modify: `llm-gateway/internal/provider/provider.go`（新增 `Embedder` 接口）
- Modify: `llm-gateway/internal/provider/openai_compat.go`（实现 `Embed`）
- Test: `llm-gateway/internal/similarity/similarity_test.go`
- Test: `llm-gateway/internal/cache/semantic_test.go`

**Interfaces:**
- Consumes: `provider.Provider`
- Produces:
  - `similarity.Cosine(a, b []float64) float64`
  - `provider.Embedder` 接口：`Embed(ctx, texts []string) ([][]float64, error)`
  - `cache.Semantic` 类型：`NewSemantic(embedder provider.Embedder, threshold float64, max int) *Semantic`、`Lookup(ctx, text string) (string, bool)`、`Store(ctx, text, answer string)`

- [ ] **Step 1: 写 cosine 失败测试**

```go
// internal/similarity/similarity_test.go
package similarity

import "testing"

func TestCosine(t *testing.T) {
	if got := Cosine([]float64{1, 0}, []float64{1, 0}); got < 0.999 {
		t.Fatalf("identical want ~1, got %v", got)
	}
	if got := Cosine([]float64{1, 0}, []float64{0, 1}); got > 0.001 {
		t.Fatalf("orthogonal want ~0, got %v", got)
	}
}
```

- [ ] **Step 2: 实现 cosine**

```go
// internal/similarity/similarity.go
package similarity

import "math"

func Cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
```

- [ ] **Step 3: 写 semantic 失败测试（fake embedder）**

```go
// internal/cache/semantic_test.go
package cache

import (
	"context"
	"testing"
)

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if t == "" {
			out[i] = []float64{0, 1}
		} else {
			out[i] = []float64{1, 0}
		}
	}
	return out, nil
}

func TestSemanticLookupHit(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	if err := s.Store(ctx, "hello world", "answer"); err != nil {
		t.Fatalf("store: %v", err)
	}
	v, ok := s.Lookup(ctx, "hi there")
	if !ok || v != "answer" {
		t.Fatalf("want hit 'answer', got %q %v", v, ok)
	}
}
```

- [ ] **Step 4: 实现 Semantic**

```go
// internal/cache/semantic.go
package cache

import (
	"context"
	"sync"

	"llm-gateway/internal/provider"
	"llm-gateway/internal/similarity"
)

type semanticEntry struct {
	vec    []float64
	answer string
}

type Semantic struct {
	mu        sync.RWMutex
	embedder  provider.Embedder
	threshold float64
	max       int
	entries   []semanticEntry
}

func NewSemantic(e provider.Embedder, threshold float64, max int) *Semantic {
	return &Semantic{embedder: e, threshold: threshold, max: max}
}

func (s *Semantic) Lookup(ctx context.Context, text string) (string, bool) {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return "", false
	}
	q := vecs[0]
	s.mu.RLock()
	defer s.mu.RUnlock()
	best, bestScore := "", 0.0
	for _, e := range s.entries {
		if sc := similarity.Cosine(q, e.vec); sc > bestScore {
			bestScore, best = sc, e.answer
		}
	}
	if bestScore >= s.threshold {
		return best, true
	}
	return "", false
}

func (s *Semantic) Store(ctx context.Context, text, answer string) error {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, semanticEntry{vec: vecs[0], answer: answer})
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
	return nil
}
```

- [ ] **Step 5: 实现 Embedder 接口与 Embed**

在 `internal/provider/provider.go` 增加：

```go
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}
```

在 `internal/provider/openai_compat.go` 增加 `Embed` 方法（调用 `{base}/embeddings`）：

```go
type oaEmbedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func (p *openAICompat) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	body, _ := json.Marshal(map[string]any{"model": p.cfg.Model, "input": texts})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	res, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("embed status %d", res.StatusCode)
	}
	var parsed oaEmbedResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([][]float64, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
```

- [ ] **Step 6: 接入 router（在精确缓存未命中后查语义缓存）**

```go
			lastUser := lastUserMessage(req.Messages)
			if answer, ok := semantic.Lookup(c.Request.Context(), lastUser); ok {
				return provider.ChatResponse{Model: req.Model, Content: answer}
			}
			// ...预算→转发...
			// 成功后：
			exact.Set(key, resp.Content)
			_ = semantic.Store(c.Request.Context(), lastUser, resp.Content)
```

`semantic := cache.NewSemantic(providers[0], 0.95, 2000)`（Embedder 用主 provider）。
新增辅助函数 `lastUserMessage`。

- [ ] **Step 7: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add -A
git commit -m "feat: 语义缓存"
```

---

### Task 9: Prometheus 指标

**Files:**
- Create: `llm-gateway/internal/metrics/metrics.go`
- Modify: `llm-gateway/internal/api/router.go`
- Test: `llm-gateway/internal/metrics/metrics_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `metrics.Registry` struct，字段 `Requests *prometheus.CounterVec`、`Latency *prometheus.HistogramVec`、`Tokens *prometheus.CounterVec`
  - `metrics.New() *Registry`
  - `Registry.Handler() gin.HandlerFunc`

- [ ] **Step 1: 写失败测试**

```go
// internal/metrics/metrics_test.go
package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsEndpointExposesCounters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := New()
	reg.Requests.WithLabelValues("deepseek", "200").Inc()

	r := gin.New()
	r.GET("/metrics", reg.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "gateway_requests_total") {
		t.Fatalf("metrics output missing counter: %s", w.Body.String())
	}
}
```

- [ ] **Step 2: 实现指标**

```go
// internal/metrics/metrics.go
package metrics

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg      *prometheus.Registry
	Requests *prometheus.CounterVec
	Latency  *prometheus.HistogramVec
	Tokens   *prometheus.CounterVec
}

func New() *Registry {
	reg := prometheus.NewRegistry()
	m := &Registry{
		reg: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total", Help: "total requests",
		}, []string{"provider", "status"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gateway_request_duration_seconds", Help: "request latency",
		}, []string{"provider"}),
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_tokens_total", Help: "tokens used",
		}, []string{"provider", "kind"}),
	}
	reg.MustRegister(m.Requests, m.Latency, m.Tokens)
	return m
}

func (m *Registry) Handler() gin.HandlerFunc {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	return func(c *gin.Context) { h.ServeHTTP(c.Writer, c.Request) }
}
```

- [ ] **Step 3: 接入 router**

在 `NewRouter` 中注册（注意 `/metrics` 不走鉴权，故注册在根路由）：

```go
	m := metrics.New()
	r.GET("/metrics", m.Handler())
```

在 chat 回调成功后埋点：

```go
	m.Requests.WithLabelValues(used.Name(), "200").Inc()
	m.Tokens.WithLabelValues(used.Name(), "prompt").Add(float64(resp.PromptTokens))
	m.Tokens.WithLabelValues(used.Name(), "completion").Add(float64(resp.CompletionTokens))
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: Prometheus 指标"
```

---

### Task 10: 用量看板页

**Files:**
- Create: `llm-gateway/web/index.html`
- Modify: `llm-gateway/internal/api/router.go`
- Test: `llm-gateway/internal/api/dashboard_test.go`

**Interfaces:**
- Consumes: `usage.Repo`
- Produces:
  - 路由 `GET /` 返回内嵌 HTML
  - 已存在的 `GET /v1/admin/usage` 作为数据源

- [ ] **Step 1: 写失败测试**

```go
// internal/api/dashboard_test.go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDashboardServesHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "LLM Gateway") {
		t.Fatalf("want dashboard html, got %d", w.Code)
	}
}
```

- [ ] **Step 2: 实现看板页**

```html
<!-- web/index.html -->
<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8" />
<title>LLM Gateway 用量看板</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
</head>
<body>
<h1>LLM Gateway</h1>
<p>本月花费：<span id="cost">-</span> 元</p>
<canvas id="chart" height="120"></canvas>
<p>请先填入网关 token 后点击刷新：</p>
<input id="token" placeholder="gateway token" />
<button onclick="load()">刷新</button>
<script>
async function load() {
  const token = document.getElementById('token').value;
  const res = await fetch('/v1/admin/usage', { headers: { Authorization: 'Bearer ' + token } });
  const data = await res.json();
  document.getElementById('cost').textContent = (data.month_cost || 0).toFixed(3);
  const labels = (data.records || []).map(r => r.CreatedAt || '');
  const costs = (data.records || []).map(r => r.Cost || 0);
  new Chart(document.getElementById('chart'), {
    type: 'line',
    data: { labels, datasets: [{ label: '单次成本', data: costs }] },
  });
}
</script>
</body>
</html>
```

- [ ] **Step 3: 实现 registerDashboard**

```go
// internal/api/dashboard.go
package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed ../../web/index.html
var dashboardHTML string

func registerDashboard(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(dashboardHTML))
	})
}
```

⚠️ `go:embed` 不能引用上层目录（`../../`）。将 `index.html` 放在 `internal/api/web/index.html`，并把指令改为：

```go
//go:embed web/index.html
var dashboardHTML string
```

相应文件路径为 `llm-gateway/internal/api/web/index.html`。

- [ ] **Step 4: 在 router 注册**

在 `NewRouter` 中：`registerDashboard(r)`。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: 极简用量看板页"
```

---

### Task 11: Docker + README + 集成测试

**Files:**
- Create: `llm-gateway/Dockerfile`
- Create: `llm-gateway/docker-compose.yml`
- Create: `llm-gateway/README.md`
- Create: `llm-gateway/tests/integration_test.go`

**Interfaces:**
- Consumes: 全部前置任务
- Produces: 可一键启动的镜像与文档

- [ ] **Step 1: 写集成测试（fake 上游 + 全链路）**

```go
// tests/integration_test.go
package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/api"
	"llm-gateway/internal/config"
)

func TestEndToEndChatWithFakeUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	s := config.Settings{
		GatewayToken: "t", DBPath: ":memory:",
		Providers: []config.ProviderConfig{{Name: "fake", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := api.NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		jsonBody(`{"model":"m","messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !contains(w.Body.String(), "pong") {
		t.Fatalf("want pong, got %s", w.Body.String())
	}
}
```

辅助函数（同文件）：

```go
func jsonBody(s string) *strings.Reader { return strings.NewReader(s) }
func contains(s, sub string) bool { return strings.Contains(s, sub) }
var _ = context.Background
```

imports 需要 `"strings"`。

- [ ] **Step 2: 运行集成测试确认通过**

Run: `go test ./tests/ -v`
Expected: PASS

- [ ] **Step 3: 写 Dockerfile**

```dockerfile
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/server /app/server
EXPOSE 8080
ENTRYPOINT ["/app/server"]
```

- [ ] **Step 4: 写 docker-compose.yml**

```yaml
services:
  gateway:
    build: .
    ports:
      - "8080:8080"
    env_file:
      - .env
    volumes:
      - gateway_data:/app/data
    restart: unless-stopped

volumes:
  gateway_data:
```

（`DB_PATH` 建议设为 `/app/data/gateway.db`。）

- [ ] **Step 5: 写 README**

README 必含：项目动机（真实痛点）、架构图、快速开始（`cp configs/.env.example .env` → 填 key → `docker compose up`）、API 示例（curl 非流式 + 流式）、`/admin/usage` 与 `/metrics` 说明、评测/演示说明。参考 spec §1 动机文案。

- [ ] **Step 6: 全量测试 + 构建验证**

Run: `go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/server && docker compose build`
Expected: 测试 PASS，构建成功

- [ ] **Step 7: 提交**

```bash
git add -A
git commit -m "feat: Docker 部署、README 与集成测试"
```

---

## 自检（Self-Review）

**1. Spec 覆盖：**
- §1 目标 1（OpenAI 兼容多 provider 转发）→ Task 2/4
- §1 目标 2（SSE 流式）→ Task 3
- §1 目标 3（故障转移）→ Task 4
- §1 目标 4（token/成本落库 + 查询）→ Task 5
- §1 目标 5（预算护栏）→ Task 7
- §1 目标 6（精确 + 语义缓存）→ Task 6/8
- §1 目标 7（Prometheus + 看板）→ Task 9/10
- §2 技术栈 → 各任务 imports
- §3 架构/目录 → 文件结构
- §4 数据流（缓存先于预算）→ Task 6/7 接入顺序
- §6 错误处理（降级、502/429/401）→ Task 1/4/7
- §7 测试策略 → 各任务测试 + Task 11 集成测试
- §8 里程碑 P0–P5 → Task 1–11

**2. 占位符扫描：** 无 TBD/TODO；每个代码步骤含实际代码。

**3. 类型一致性：**
- `provider.Provider`（`Name`/`Chat`/`ChatStream`）在 Task 2/3/4 一致
- `provider.ChatRequest` 在 Task 3 增加 `Stream bool`，Task 6 的 `cache.Exact.Key` 仅用 `Model`/`Messages`，不受影响
- `usage.Repo` 在 Task 5 定义，Task 5/7/10 一致使用
- `budget.Decision` 常量在 Task 7 定义并一致使用
- `embedder` 作为 `provider.Embedder` 传入 `cache.Semantic`（Task 8）

**已知实现注意点（供执行者留意，非占位符）：**
- Task 10 `go:embed` 需将 HTML 放于 `internal/api/web/`，不可用 `../../`。
- Task 5 `/admin/usage` 返回的 `records` 字段为小写 JSON key（Go 默认按字段名序列化），前端 Task 10 使用 `r.Cost`/`r.CreatedAt` 前需为 `usage.Record` 增加 json tag（`cost`、`created_at`）或在 handler 中转换。执行 Task 5 时请为 `usage.Record` 补上 json tag：`Cost float64 \`json:"cost"\``、`CreatedAt time.Time \`json:"created_at"\``。