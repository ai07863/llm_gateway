# Responses API 透传实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `POST /v1/responses` 透明透传，复用现有鉴权、路由、预算、失败转移、usage 记账与看板，使 Codex 0.166 可用 `wire_api = "responses"`。

**Architecture:** provider 层新增可选 `ResponsesProvider` 接口并让 `openAICompat` 实现非流式/流式转发；proxy 层新增对应 failover 辅助函数；api 层新增 handler 解析 `model`/`stream` 并原样转发，流式逐帧透传 SSE；router 注入预算、路由、记账与 metrics 依赖。不做缓存，不新增环境变量。

**Tech Stack:** Go 1.22+、Gin、net/http、httptest、SQLite（现有 usage）。

**Spec:** `docs/superpowers/specs/2026-11-02-responses-api-design.md`

## Global Constraints

- 不新增环境变量或配置项。
- 不实现缓存；`CacheHit` 恒为 false。
- 不改变 `/v1/chat/completions` 与 `/v1/embeddings` 的现有行为。
- 请求体除顶层 `model` 外原样转发；流式 SSE 帧（含 `id:`、`event:`、`:HTTP_STATUS/200`）原样转发，不追加 `[DONE]`。
- 上游 4xx 透传状态码与 body；5xx/网络重试耗尽后返回 502；客户端断开不记账。
- 定价沿用 `pricing.Cost`，包含高峰/闲时。
- 提交信息使用 `type(scope): 英文描述`。

---

### Task 1: Provider 非流式 Responses 转发

**Files:**
- Modify: `internal/provider/errors.go`
- Create: `internal/provider/responses.go`
- Test: `internal/provider/responses_test.go`

**Interfaces:**
- Consumes: `config.ProviderConfig`、`openAICompat.client`、`NewStatusError`、`NewNetworkError`
- Produces:
  - `type ResponsesRequest struct { Model string; Stream bool; Raw []byte }`
  - `type ResponsesResult struct { Model string; PromptTokens, CompletionTokens, TotalTokens int; Raw []byte }`
  - `type ResponsesChunk struct { Raw string; Done bool; PromptTokens, CompletionTokens, TotalTokens int; Err error }`
  - `type ResponsesProvider interface { Responses(ctx, ResponsesRequest) (ResponsesResult, error); ResponsesStream(ctx, ResponsesRequest) (<-chan ResponsesChunk, error) }`
  - `func (p *openAICompat) Responses(ctx context.Context, req ResponsesRequest) (ResponsesResult, error)`
  - `provider.Error.Body string`（上游原始响应体）

- [ ] **Step 1: 写失败测试**

创建 `internal/provider/responses_test.go`：

```go
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"llm-gateway/internal/config"
)

type responsesCaller interface {
	Responses(context.Context, ResponsesRequest) (ResponsesResult, error)
}

func TestOpenAICompatResponsesRewritesModelAndParsesUsage(t *testing.T) {
	const upstreamBody = `{"id":"resp_1","object":"response","model":"m2","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m1"}).(responsesCaller)
	res, err := p.Responses(context.Background(), ResponsesRequest{
		Model: "m2",
		Raw:   []byte(`{"model":"m1","input":"hi","custom":{"keep":true}}`),
	})
	if err != nil {
		t.Fatalf("responses error: %v", err)
	}
	if string(got["model"]) != `"m2"` {
		t.Fatalf("upstream model must be rewritten to m2, got %s", got["model"])
	}
	if string(got["custom"]) != `{"keep":true}` {
		t.Fatalf("unknown field must survive, got %s", got["custom"])
	}
	if res.PromptTokens != 11 || res.CompletionTokens != 7 || res.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", res)
	}
	if string(res.Raw) != upstreamBody {
		t.Fatalf("raw body must pass through unchanged, got %q", res.Raw)
	}
}

func TestOpenAICompatResponses4xxExposesBodyAndIsNotRetryable(t *testing.T) {
	const errBody = `{"error":{"message":"bad input"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, errBody)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(responsesCaller)
	_, err := p.Responses(context.Background(), ResponsesRequest{Model: "m", Raw: []byte(`{"model":"m","input":"hi"}`)})
	if err == nil {
		t.Fatal("want error")
	}
	if IsRetryable(err) {
		t.Fatalf("4xx must not be retryable: %v", err)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *provider.Error, got %T", err)
	}
	if pe.StatusCode != http.StatusBadRequest || pe.Body != errBody {
		t.Fatalf("want status 400 and upstream body, got %d %q", pe.StatusCode, pe.Body)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/provider -run 'Responses' -v`
Expected: 编译失败（`ResponsesRequest`/`ResponsesProvider` 未定义）。

- [ ] **Step 3: 给 provider.Error 增加 Body 字段**

修改 `internal/provider/errors.go` 的结构体并设置 `Body: body`：

```go
type Error struct {
	Provider   string
	StatusCode int
	Retryable  bool
	Body       string
	Err        error
}

func NewStatusError(providerName string, status int, body string) *Error {
	return &Error{
		Provider:   providerName,
		StatusCode: status,
		Retryable:  status == 429 || status >= 500,
		Body:       body,
		Err:        fmt.Errorf("provider %s status %d: %s", providerName, status, body),
	}
}
```

- [ ] **Step 4: 实现非流式 Responses**

创建 `internal/provider/responses.go`：

```go
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type ResponsesRequest struct {
	Model  string
	Stream bool
	Raw    []byte
}

type ResponsesResult struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Raw              []byte
}

type ResponsesChunk struct {
	Raw              string
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

type responsesPayload struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// prepareResponsesBody rewrites the top-level model and, for streams, forces
// stream=true while leaving every other client field untouched.
func prepareResponsesBody(raw []byte, model string, stream bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	mb, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = mb
	if stream {
		m["stream"] = json.RawMessage("true")
	}
	return json.Marshal(m)
}

func (p *openAICompat) Responses(ctx context.Context, req ResponsesRequest) (ResponsesResult, error) {
	body, err := prepareResponsesBody(req.Raw, req.Model, false)
	if err != nil {
		return ResponsesResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return ResponsesResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return ResponsesResult{}, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ResponsesResult{}, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}
	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return ResponsesResult{}, err
	}
	var parsed responsesPayload
	_ = json.Unmarshal(respBody, &parsed)
	model := parsed.Model
	if model == "" {
		model = req.Model
	}
	return ResponsesResult{
		Model:            model,
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		Raw:              respBody,
	}, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/provider -run 'Responses' -v`
Expected: 两个测试 PASS；`go test ./internal/provider` 全绿。

- [ ] **Step 6: 提交**

```bash
git add internal/provider/errors.go internal/provider/responses.go internal/provider/responses_test.go
git commit -m "feat(provider): add non-stream responses passthrough"
```

---

### Task 2: Provider 流式 Responses 转发

**Files:**
- Modify: `internal/provider/responses.go`
- Test: `internal/provider/responses_test.go`

**Interfaces:**
- Consumes: `prepareResponsesBody`、`openAICompat.streamClient`
- Produces: `func (p *openAICompat) ResponsesStream(ctx context.Context, req ResponsesRequest) (<-chan ResponsesChunk, error)`；每个 `ResponsesChunk.Raw` 是完整一帧（多行 + 结尾空行）。

- [ ] **Step 1: 写失败测试**

追加到 `internal/provider/responses_test.go`（并把 `strings` 加入 import）：

```go
func TestOpenAICompatResponsesStreamForwardsFramesAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if string(body["model"]) != `"m2"` {
			t.Errorf("model not rewritten: %s", body["model"])
		}
		if string(body["stream"]) != "true" {
			t.Errorf("stream not forced: %s", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id:1\nevent:response.created\n:HTTP_STATUS/200\ndata:{\"type\":\"response.created\"}\n\n")
		_, _ = io.WriteString(w, "id:2\nevent:response.completed\n:HTTP_STATUS/200\ndata:{\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m1"}).(ResponsesProvider)
	ch, err := p.ResponsesStream(context.Background(), ResponsesRequest{Model: "m2", Stream: true, Raw: []byte(`{"model":"m1","input":"hi"}`)})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var raw strings.Builder
	var done ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
			break
		}
		raw.WriteString(c.Raw)
	}
	if !strings.Contains(raw.String(), "event: response.created") || !strings.Contains(raw.String(), ":HTTP_STATUS/200") {
		t.Fatalf("frames must be forwarded verbatim, got %q", raw.String())
	}
	if done.Err != nil {
		t.Fatalf("unexpected done error: %v", done.Err)
	}
	if done.PromptTokens != 5 || done.CompletionTokens != 2 || done.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", done)
	}
}

func TestOpenAICompatResponsesStreamEarlyEOFIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id:1\nevent:response.created\ndata:{\"type\":\"response.created\"}\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(ResponsesProvider)
	ch, err := p.ResponsesStream(context.Background(), ResponsesRequest{Model: "m", Stream: true, Raw: []byte(`{"model":"m","input":"hi"}`)})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	var done ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
		}
	}
	if done.Err == nil {
		t.Fatal("want error when upstream closes before response.completed")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/provider -run 'ResponsesStream' -v`
Expected: `ResponsesStream` 未定义，编译失败。

- [ ] **Step 3: 实现流式转发**

追加到 `internal/provider/responses.go`（补充 import：`bufio`、`errors`、`strings`）：

```go
type responsesEvent struct {
	Type     string `json:"type"`
	Response struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

func (p *openAICompat) ResponsesStream(ctx context.Context, req ResponsesRequest) (<-chan ResponsesChunk, error) {
	body, err := prepareResponsesBody(req.Raw, req.Model, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, NewNetworkError(p.cfg.Name, err)
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}

	ch := make(chan ResponsesChunk)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		send := func(c ResponsesChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}

		var frame strings.Builder
		var prompt, completion, total int
		completed := false
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		flush := func() bool {
			if frame.Len() == 0 {
				return true
			}
			raw := frame.String() + "\n"
			frame.Reset()
			return send(ResponsesChunk{Raw: raw})
		}

		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if line == "" {
				if !flush() {
					return
				}
				continue
			}
			frame.WriteString(line)
			frame.WriteByte('\n')
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				var ev responsesEvent
				if err := json.Unmarshal([]byte(payload), &ev); err == nil && ev.Type == "response.completed" {
					completed = true
					prompt = ev.Response.Usage.InputTokens
					completion = ev.Response.Usage.OutputTokens
					total = ev.Response.Usage.TotalTokens
				}
			}
		}
		flush()
		if err := scanner.Err(); err != nil {
			send(ResponsesChunk{Done: true, Err: err, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		if !completed {
			send(ResponsesChunk{Done: true, Err: errors.New("upstream stream ended before response.completed"), PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		send(ResponsesChunk{Done: true, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
	}()
	return ch, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/provider -run 'Responses' -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/provider/responses.go internal/provider/responses_test.go
git commit -m "feat(provider): add streaming responses passthrough"
```

---

### Task 3: Proxy Responses Failover

**Files:**
- Create: `internal/proxy/responses.go`
- Test: `internal/proxy/responses_test.go`

**Interfaces:**
- Consumes: `Target`、`doWithRetry`、`provider.ResponsesProvider`、`provider.IsRetryable`
- Produces:
  - `func ResponsesWithFailover(ctx, targets []Target, req provider.ResponsesRequest) (provider.ResponsesResult, Target, error)`
  - `func ResponsesStreamWithFailover(ctx, targets []Target, req provider.ResponsesRequest) (<-chan provider.ResponsesChunk, Target, error)`
- 语义与 `ChatWithFailover`/`StreamWithFailover` 一致：不可重试错误立即返回，可重试错误按 `MaxAttempts` 重试后切换下一个 target；流式只在首帧前失败转移。

- [ ] **Step 1: 写失败测试**

创建 `internal/proxy/responses_test.go`：

```go
package proxy

import (
	"context"
	"errors"
	"testing"

	"llm-gateway/internal/provider"
)

type responseProvider struct {
	result provider.ResponsesResult
	err    error
	calls  int
	last   provider.ResponsesRequest
}

func (f *responseProvider) Name() string      { return "fake" }
func (f *responseProvider) ChatModel() string { return "fake-model" }
func (f *responseProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("not implemented")
}
func (f *responseProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("not implemented")
}
func (f *responseProvider) Responses(ctx context.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return provider.ResponsesResult{}, f.err
	}
	res := f.result
	if res.Model == "" {
		res.Model = req.Model
	}
	return res, nil
}
func (f *responseProvider) ResponsesStream(ctx context.Context, req provider.ResponsesRequest) (<-chan provider.ResponsesChunk, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan provider.ResponsesChunk, 2)
	ch <- provider.ResponsesChunk{Raw: "data: ok\n\n"}
	ch <- provider.ResponsesChunk{Done: true, TotalTokens: 7}
	close(ch)
	return ch, nil
}

func TestResponsesWithFailoverRetriesThenSucceeds(t *testing.T) {
	fastRetry(t)
	old := MaxAttempts
	MaxAttempts = 1
	t.Cleanup(func() { MaxAttempts = old })

	bad := &responseProvider{err: provider.NewStatusError("bad", 500, "boom")}
	good := &responseProvider{result: provider.ResponsesResult{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7, Raw: []byte(`{"id":"ok"}`)}}
	targets := []Target{{Provider: bad, Model: "primary-m"}, {Provider: good, Model: "backup-m"}}

	res, used, err := ResponsesWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "primary-m", Raw: []byte(`{"model":"primary-m"}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Fatalf("want one call each, got bad=%d good=%d", bad.calls, good.calls)
	}
	if used.Model != "backup-m" || good.last.Model != "backup-m" {
		t.Fatalf("want backup target, got used=%s req=%s", used.Model, good.last.Model)
	}
	if string(good.last.Raw) != `{"model":"primary-m"}` {
		t.Fatalf("raw body must not be mutated, got %s", good.last.Raw)
	}
	if res.PromptTokens != 3 || res.CompletionTokens != 4 || res.TotalTokens != 7 || string(res.Raw) != `{"id":"ok"}` {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestResponsesWithFailoverNonRetryableStopsImmediately(t *testing.T) {
	fastRetry(t)
	primary := &responseProvider{err: provider.NewStatusError("primary", 400, "bad")}
	backup := &responseProvider{}
	targets := []Target{{Provider: primary, Model: "m"}, {Provider: backup, Model: "b"}}

	if _, _, err := ResponsesWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "m"}); err == nil {
		t.Fatal("want non-retryable error")
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("non-retryable must not retry or fail over: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

func TestResponsesStreamWithFailoverRetriesBeforeFirstChunk(t *testing.T) {
	fastRetry(t)
	old := MaxAttempts
	MaxAttempts = 1
	t.Cleanup(func() { MaxAttempts = old })

	bad := &responseProvider{err: provider.NewStatusError("bad", 503, "unavailable")}
	good := &responseProvider{}
	targets := []Target{{Provider: bad, Model: "primary-m"}, {Provider: good, Model: "backup-m"}}

	ch, used, err := ResponsesStreamWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "primary-m", Stream: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw string
	var done provider.ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
			continue
		}
		raw += c.Raw
	}
	if used.Model != "backup-m" || raw != "data: ok\n\n" || done.TotalTokens != 7 {
		t.Fatalf("unexpected stream: used=%s raw=%q done=%+v", used.Model, raw, done)
	}
}

func TestResponsesFailoverEmptyTargetsReturnsError(t *testing.T) {
	if _, _, err := ResponsesWithFailover(context.Background(), nil, provider.ResponsesRequest{}); err == nil {
		t.Fatal("want error for empty targets")
	}
	if _, _, err := ResponsesStreamWithFailover(context.Background(), nil, provider.ResponsesRequest{}); err == nil {
		t.Fatal("want error for empty targets")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/proxy -run 'Responses' -v`
Expected: `ResponsesWithFailover` 未定义，编译失败。

- [ ] **Step 3: 实现 failover 辅助函数**

创建 `internal/proxy/responses.go`：

```go
package proxy

import (
	"context"
	"errors"

	"llm-gateway/internal/provider"
)

func ResponsesWithFailover(ctx context.Context, targets []Target, req provider.ResponsesRequest) (provider.ResponsesResult, Target, error) {
	if len(targets) == 0 {
		return provider.ResponsesResult{}, Target{}, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return provider.ResponsesResult{}, Target{}, ctx.Err()
		}
		rp, ok := tgt.Provider.(provider.ResponsesProvider)
		if !ok {
			continue
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		res, err := doWithRetry(ctx, func() (provider.ResponsesResult, error) {
			return rp.Responses(ctx, attemptReq)
		})
		if err == nil {
			res.Model = tgt.Model
			return res, tgt, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return provider.ResponsesResult{}, Target{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no provider supports the responses API")
	}
	return provider.ResponsesResult{}, Target{}, lastErr
}

func ResponsesStreamWithFailover(ctx context.Context, targets []Target, req provider.ResponsesRequest) (<-chan provider.ResponsesChunk, Target, error) {
	if len(targets) == 0 {
		return nil, Target{}, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, Target{}, ctx.Err()
		}
		rp, ok := tgt.Provider.(provider.ResponsesProvider)
		if !ok {
			continue
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		ch, err := doWithRetry(ctx, func() (<-chan provider.ResponsesChunk, error) {
			return rp.ResponsesStream(ctx, attemptReq)
		})
		if err == nil {
			return ch, tgt, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return nil, Target{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no provider supports the responses API")
	}
	return nil, Target{}, lastErr
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/proxy -run 'Responses' -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/proxy/responses.go internal/proxy/responses_test.go
git commit -m "feat(proxy): add responses failover helpers"
```

---

### Task 4: API Handler 与错误映射

**Files:**
- Create: `internal/api/responses.go`
- Test: `internal/api/responses_test.go`

**Interfaces:**
- Consumes: `provider.ResponsesRequest`、`provider.ResponsesResult`、`provider.ResponsesChunk`、`provider.Error`、`StatusError`、`mustJSON`
- Produces:
  - `func registerResponses(v1 *gin.RouterGroup, responses func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error), responsesStream func(c *gin.Context, req provider.ResponsesRequest) error)`
  - `func writeResponsesError(c *gin.Context, err error)`
  - `func writeResponsesSSE(c *gin.Context, ch <-chan provider.ResponsesChunk) (promptTokens, completionTokens int, err error)`

- [ ] **Step 1: 写失败测试**

创建 `internal/api/responses_test.go`：

```go
package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

func newResponsesTestRouter(
	responses func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error),
	stream func(c *gin.Context, req provider.ResponsesRequest) error,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerResponses(r.Group("/v1"), responses, stream)
	return r
}

func TestRegisterResponsesForwardsNonStream(t *testing.T) {
	var got provider.ResponsesRequest
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		got = req
		return provider.ResponsesResult{Model: req.Model, Raw: []byte(`{"id":"resp_1"}`)}, nil
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi","custom":true}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"id":"resp_1"}` || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %q %q", w.Body.String(), w.Header().Get("Content-Type"))
	}
	if got.Model != "m1" || got.Stream || !strings.Contains(string(got.Raw), `"custom":true`) {
		t.Fatalf("unexpected request: %+v", got)
	}
}

func TestRegisterResponsesStreamsFramesVerbatim(t *testing.T) {
	r := newResponsesTestRouter(nil, func(c *gin.Context, req provider.ResponsesRequest) error {
		ch := make(chan provider.ResponsesChunk, 2)
		ch <- provider.ResponsesChunk{Raw: "id:1\nevent:response.created\ndata:{\"type\":\"response.created\"}\n\n"}
		ch <- provider.ResponsesChunk{Raw: "id:2\nevent:response.completed\ndata:{\"type\":\"response.completed\"}\n\n", Done: true}
		close(ch)
		_, _, err := writeResponsesSSE(c, ch)
		return err
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi","stream":true}`))
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "event: response.created") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("frames not forwarded: %q", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("responses stream must not append [DONE]: %q", body)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type = %q", w.Header().Get("Content-Type"))
	}
}

func TestRegisterResponsesPassesUpstream4xxBody(t *testing.T) {
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		return provider.ResponsesResult{}, provider.NewStatusError("upstream", http.StatusBadRequest, `{"error":{"message":"bad"}}`)
	}, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m1","input":"hi"}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest || w.Body.String() != `{"error":{"message":"bad"}}` {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestRegisterResponsesRejectsBadJSONAndMissingModel(t *testing.T) {
	r := newResponsesTestRouter(func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
		t.Fatal("handler must not be called")
		return provider.ResponsesResult{}, nil
	}, nil)

	for _, body := range []string{`{`, `{"input":"hi"}`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(body))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d", body, w.Code)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/api -run 'RegisterResponses' -v`
Expected: `registerResponses` 未定义，编译失败。

- [ ] **Step 3: 实现 handler 与错误映射**

创建 `internal/api/responses.go`：

```go
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type responsesProbe struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func registerResponses(
	v1 *gin.RouterGroup,
	responses func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error),
	responsesStream func(c *gin.Context, req provider.ResponsesRequest) error,
) {
	v1.POST("/responses", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		var probe responsesProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		if probe.Model == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "model is required"}})
			return
		}
		req := provider.ResponsesRequest{Model: probe.Model, Stream: probe.Stream, Raw: raw}
		if probe.Stream {
			if err := responsesStream(c, req); err != nil {
				writeResponsesError(c, err)
			}
			return
		}
		res, err := responses(c, req)
		if err != nil {
			writeResponsesError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json", res.Raw)
	})
}

// writeResponsesError mirrors writeError but passes a 4xx upstream body through
// untouched so clients see the provider's native error shape.
func writeResponsesError(c *gin.Context, err error) {
	var pe *provider.Error
	if errors.As(err, &pe) && pe.StatusCode >= 400 && pe.StatusCode < 500 && pe.Body != "" {
		c.Data(pe.StatusCode, "application/json", []byte(pe.Body))
		return
	}
	var se *StatusError
	if errors.As(err, &se) {
		c.JSON(se.Status, gin.H{"error": gin.H{"message": se.Msg}})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
}

// writeResponsesSSE forwards frames verbatim. Responses streams are terminated
// by response.completed, so no [DONE] sentinel is appended.
func writeResponsesSSE(c *gin.Context, ch <-chan provider.ResponsesChunk) (int, int, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	var prompt, completion int
	var streamErr error
	c.Stream(func(w io.Writer) bool {
		chunk, ok := <-ch
		if !ok {
			return false
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(gin.H{"error": gin.H{"message": chunk.Err.Error()}}))
			return false
		}
		if chunk.Raw != "" {
			fmt.Fprint(w, chunk.Raw)
		}
		if chunk.Done {
			prompt, completion = chunk.PromptTokens, chunk.CompletionTokens
			return false
		}
		return true
	})
	return prompt, completion, streamErr
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/api -run 'RegisterResponses' -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/api/responses.go internal/api/responses_test.go
git commit -m "feat(api): add responses endpoint handler"
```

---

### Task 5: Router 接线与端到端测试

**Files:**
- Modify: `internal/api/router.go`
- Test: `internal/api/responses_router_test.go`

**Interfaces:**
- Consumes: `registerResponses`、`proxy.ResponsesWithFailover`、`proxy.ResponsesStreamWithFailover`、`targetsForModel`、`budget.Evaluate`、`pricing.Cost`、`pricing.Score`、`budget.CheaperModel`、`recordUsage`、`m`
- Produces: `POST /v1/responses` 在真实路由器上可用，复用预算拒绝/降级、失败转移、usage 记账、latency/requests/tokens 指标。

- [ ] **Step 1: 写失败测试**

创建 `internal/api/responses_router_test.go`：

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

func TestResponsesEndpointFailsOverAndPassesThrough(t *testing.T) {
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
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		backupModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"id":"resp_1","model":"backup-model","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	}))
	defer backup.Close()

	s := config.Settings{
		GatewayToken: "tok", DBPath: "file:responses_router?mode=memory&cache=shared", BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{
			{Name: "primary", BaseURL: primary.URL, APIKey: "k", Model: "primary-model"},
			{Name: "backup", BaseURL: backup.URL, APIKey: "k", Model: "backup-model"},
		},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"primary-model","input":"hi"}`))
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
	if !strings.Contains(w.Body.String(), `"id":"resp_1"`) {
		t.Fatalf("body not passed through: %q", w.Body.String())
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/api -run 'ResponsesEndpoint' -v`
Expected: 404（路由未注册），断言 200 失败。

- [ ] **Step 3: 在 router.go 接线**

在 `registerChat(...)` 调用之后、`return r` 之前追加：

```go
	registerResponses(v1,
		func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
			cost, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
			switch budget.Evaluate(cost, s.BudgetMonthlyLimit, s.BudgetSoftRatio) {
			case budget.Reject:
				m.Requests.WithLabelValues("budget", "429").Inc()
				return provider.ResponsesResult{}, &StatusError{Status: http.StatusTooManyRequests, Msg: "monthly budget exceeded"}
			case budget.Degrade:
				score, _ := pricing.Score(req.Model)
				req.Model = budget.CheaperModel(req.Model, score, modelCandidates)
			}
			if len(providers) == 0 {
				m.Requests.WithLabelValues("none", "502").Inc()
				return provider.ResponsesResult{}, errors.New("no provider configured")
			}
			start := time.Now()
			res, used, err := proxy.ResponsesWithFailover(c.Request.Context(), targetsForModel(req.Model), req)
			if err != nil {
				m.Requests.WithLabelValues("unknown", "502").Inc()
				return provider.ResponsesResult{}, err
			}
			name := used.Provider.Name()
			recordUsage(req.Model, name, res.PromptTokens, res.CompletionTokens)
			m.Latency.WithLabelValues(name).Observe(time.Since(start).Seconds())
			m.Requests.WithLabelValues(name, "200").Inc()
			m.Tokens.WithLabelValues(name, "prompt").Add(float64(res.PromptTokens))
			m.Tokens.WithLabelValues(name, "completion").Add(float64(res.CompletionTokens))
			return res, nil
		},
		func(c *gin.Context, req provider.ResponsesRequest) error {
			cost, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
			switch budget.Evaluate(cost, s.BudgetMonthlyLimit, s.BudgetSoftRatio) {
			case budget.Reject:
				m.Requests.WithLabelValues("budget", "429").Inc()
				return &StatusError{Status: http.StatusTooManyRequests, Msg: "monthly budget exceeded"}
			case budget.Degrade:
				score, _ := pricing.Score(req.Model)
				req.Model = budget.CheaperModel(req.Model, score, modelCandidates)
			}
			if len(providers) == 0 {
				m.Requests.WithLabelValues("none", "502").Inc()
				return errors.New("no provider configured")
			}
			start := time.Now()
			ch, used, err := proxy.ResponsesStreamWithFailover(c.Request.Context(), targetsForModel(req.Model), req)
			if err != nil {
				m.Requests.WithLabelValues("unknown", "502").Inc()
				return err
			}
			name := used.Provider.Name()
			prompt, completion, streamErr := writeResponsesSSE(c, ch)
			switch {
			case streamErr != nil:
				m.Requests.WithLabelValues(name, "500").Inc()
			case c.Request.Context().Err() != nil:
				m.Requests.WithLabelValues(name, "canceled").Inc()
			default:
				recordUsage(req.Model, name, prompt, completion)
				m.Latency.WithLabelValues(name).Observe(time.Since(start).Seconds())
				m.Requests.WithLabelValues(name, "200").Inc()
				m.Tokens.WithLabelValues(name, "prompt").Add(float64(prompt))
				m.Tokens.WithLabelValues(name, "completion").Add(float64(completion))
			}
			return nil
		},
	)
```

- [ ] **Step 4: 运行全部 API 测试**

Run: `go test ./internal/api -v`
Expected: 全部 PASS（含新增 `TestResponsesEndpointFailsOverAndPassesThrough`）。

- [ ] **Step 5: 提交**

```bash
git add internal/api/router.go internal/api/responses_router_test.go
git commit -m "feat(api): wire responses route with budget and accounting"
```

---

### Task 6: 文档与全量验证

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 更新 README 端点表**

在端点列表中加入：

```markdown
| `POST /v1/responses` | OpenAI Responses API 透传（支持流式与工具调用） |
```

- [ ] **Step 2: 全量格式、静态检查与测试**

```bash
gofmt -l .
go vet ./...
go test ./...
go build -o gateway-next.exe .
```

Expected: `gofmt -l .` 无输出；`go vet`、`go test`、`go build` 全部成功。

- [ ] **Step 3: 提交**

```bash
git add README.md
git commit -m "docs: document responses endpoint"
```

---

### Task 7: 线上验收（临时实例 8091）

**Files:** 无代码改动。

- [ ] **Step 1: 启动临时实例**

```powershell
go build -o gateway-next.exe .
$env:LISTEN_ADDR=":8091"; $env:DB_PATH="gateway-next.db"; $env:BUDGET_MONTHLY_LIMIT="0"
Start-Process -FilePath ".\gateway-next.exe" -WindowStyle Hidden
Start-Sleep -Seconds 2
Invoke-RestMethod http://127.0.0.1:8091/healthz
```

Expected: `status = ok`。

- [ ] **Step 2: 非流式与流式冒烟**

```powershell
$tok = "68f0d3c4d9aacb18027b382b21528bf7cb57252a44022e6cbd8ffc1cdd084be3"
$body = '{"model":"deepseek-v4.1-flash-1017","input":"reply with OK"}'
curl.exe -sS -i http://127.0.0.1:8091/v1/responses -H "Authorization: Bearer $tok" -H "Content-Type: application/json" -d $body
$bodyStream = '{"model":"deepseek-v4.1-flash-1017","input":"reply with OK","stream":true}'
curl.exe -sS -N http://127.0.0.1:8091/v1/responses -H "Authorization: Bearer $tok" -H "Content-Type: application/json" -d $bodyStream
```

Expected: 非流式 200 且含 `"object":"response"`；流式含 `event: response.completed` 与 `"usage"`。

- [ ] **Step 3: Codex 0.166 独立 CODEX_HOME 验收**

```powershell
$tmp = Join-Path $env:TEMP "codex-responses-acceptance"
New-Item -ItemType Directory -Force $tmp | Out-Null
@'
model = "deepseek-v4.1-flash-1017"
model_provider = "gateway"

[model_providers.gateway]
name = "gateway"
base_url = "http://127.0.0.1:8091/v1"
env_key = "CODEX_GATEWAY_TOKEN"
wire_api = "responses"
'@ | Set-Content -Encoding UTF8 (Join-Path $tmp "config.toml")

$env:CODEX_HOME = $tmp
$env:CODEX_GATEWAY_TOKEN = "68f0d3c4d9aacb18027b382b21528bf7cb57252a44022e6cbd8ffc1cdd084be3"
npx -y @openai/codex@0.166.0 exec --skip-git-repo-check "Run the shell command echo responses-ok and report its output."
```

Expected: 对话完成并实际执行 `echo responses-ok`，输出包含 `responses-ok`；网关日志无 404/500。

- [ ] **Step 4: 清理临时实例**

```powershell
Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -like '*gateway-next.exe*' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
Remove-Item -LiteralPath .\gateway-next.exe, .\gateway-next.db -ErrorAction SilentlyContinue
Remove-Item -Recurse -Force (Join-Path $env:TEMP "codex-responses-acceptance") -ErrorAction SilentlyContinue
```

- [ ] **Step 5: 确认主网关未受影响**

```powershell
Invoke-RestMethod http://127.0.0.1:8090/healthz
```

Expected: 主网关（8090）仍返回 `ok`。

---

## Self-Review

- **Spec coverage:** 非流式/流式透传、模型改写、4xx body 透传、5xx failover、预算拒绝/降级、usage 记账、不追加 `[DONE]`、不做缓存、不新增环境变量均已覆盖。
- **Placeholder scan:** 无 TBD/TODO；每个任务含失败测试、实现、验证命令与提交步骤。
- **Type consistency:** `writeResponsesSSE` 返回 `(prompt, completion, error)` 并在 Task 4/5 一致使用；`ResponsesWithFailover`/`ResponsesStreamWithFailover` 返回 `(…, Target, error)`；`Target.Provider` 为 `provider.Provider`，router 通过 `used.Provider.Name()` 取名。
- **已知风险:** 若上游在流式响应中途断开，客户端已收到部分帧，只能发送 `event: error`；Codex 0.166 的验收是最终判据。