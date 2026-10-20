package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

func sseHandler(tokens []string, usage string, done bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, tok := range tokens {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tok)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if usage != "" {
			fmt.Fprintf(w, "data: %s\n\n", usage)
		}
		if done {
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func TestChatStreamServesExactCacheHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"content":"cached answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             "file:stream_cache?mode=memory&cache=shared",
		BudgetMonthlyLimit: 100,
		Providers:          []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := NewRouter(s)

	seed := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	seed.Header.Set("Content-Type", "application/json")
	seed.Header.Set("Authorization", "Bearer tok")
	sw := httptest.NewRecorder()
	r.ServeHTTP(sw, seed)
	if sw.Code != http.StatusOK {
		t.Fatalf("seed request want 200, got %d body=%q", sw.Code, sw.Body.String())
	}

	stream := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	stream.Header.Set("Content-Type", "application/json")
	stream.Header.Set("Authorization", "Bearer tok")
	w := newCloseNotifierRecorder()
	r.ServeHTTP(w, stream)

	if w.Code != http.StatusOK {
		t.Fatalf("stream want 200, got %d body=%q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cached answer") {
		t.Fatalf("stream cache hit should serve cached content, body=%q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("want SSE done marker, body=%q", w.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream should be hit once (seed only), got %d", got)
	}
}

func TestChatStreamBudgetRejectReturns429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := "file:stream_reject?mode=memory&cache=shared"
	s := config.Settings{GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 1, BudgetSoftRatio: 0.8}
	r := NewRouter(s)

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	if err := repo.Record(context.Background(), usage.Record{ID: "seed", Model: "m", Cost: 5}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d; body=%q", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not a single valid JSON document: %v; body=%q", err, w.Body.String())
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object, body=%q", w.Body.String())
	}
	if errObj["message"] != "monthly budget exceeded" {
		t.Fatalf("want budget message, got %v", errObj["message"])
	}
}

func TestChatStreamBudgetDegradeSwapsModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel, _ = req["model"].(string)
		sseHandler([]string{"ok"}, "", true)(w, r)
	}))
	defer upstream.Close()

	db := "file:stream_degrade?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             db,
		BudgetMonthlyLimit: 10,
		BudgetSoftRatio:    0.5,
		Providers:          []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"}},
	}
	r := NewRouter(s)

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	// cost 6 >= soft (5) and < limit (10) → Degrade.
	if err := repo.Record(context.Background(), usage.Record{ID: "seed", Model: "m", Cost: 6}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := newCloseNotifierRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%q", w.Code, w.Body.String())
	}
	if gotModel != "deepseek-chat" {
		t.Fatalf("want degraded model deepseek-chat upstream, got %q", gotModel)
	}
}

func TestChatStreamRecordsUsageAndMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(sseHandler([]string{"he", "llo"},
		`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`, true))
	defer upstream.Close()

	db := "file:stream_metrics?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             db,
		BudgetMonthlyLimit: 100,
		Providers:          []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := newCloseNotifierRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%q", w.Code, w.Body.String())
	}

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	recs, err := repo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 usage record, got %d", len(recs))
	}
	if recs[0].PromptTokens != 4 || recs[0].CompletionTokens != 3 || recs[0].Provider != "up" {
		t.Fatalf("unexpected usage record: %+v", recs[0])
	}

	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := mw.Body.String()
	if !strings.Contains(out, `gateway_requests_total{provider="up",status="200"} 1`) {
		t.Fatalf("stream requests counter not recorded: %s", out)
	}
	if !strings.Contains(out, `gateway_tokens_total{kind="prompt",provider="up"} 4`) {
		t.Fatalf("stream prompt tokens not recorded: %s", out)
	}
	if !strings.Contains(out, `gateway_tokens_total{kind="completion",provider="up"} 3`) {
		t.Fatalf("stream completion tokens not recorded: %s", out)
	}
}

func TestChatStreamWritesSSEErrorOnUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(sseHandler([]string{"partial"}, "", false))
	defer upstream.Close()

	db := "file:stream_error?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             db,
		BudgetMonthlyLimit: 100,
		Providers:          []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := newCloseNotifierRecorder()
	r.ServeHTTP(w, req)
	out := w.Body.String()

	if !strings.Contains(out, `"error"`) {
		t.Fatalf("want SSE error event, body=%q", out)
	}
	if !strings.Contains(out, "partial") {
		t.Fatalf("want the partial content before the error, body=%q", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("want SSE done marker after error, body=%q", out)
	}
}
