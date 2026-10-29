package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

func TestChatPassesThroughToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotTools bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, gotTools = body["tools"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()

	s := config.Settings{
		GatewayToken: "tok", DBPath: "file:passthrough?mode=memory&cache=shared", BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := NewRouter(s)

	body := `{"model":"m","messages":[{"role":"user","content":"read x"}],"tools":[{"type":"function","function":{"name":"read","parameters":{}}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !gotTools {
		t.Fatal("upstream did not receive tools")
	}
	if !strings.Contains(w.Body.String(), "tool_calls") {
		t.Fatalf("response lost tool_calls: %s", w.Body.String())
	}
}

func TestToolRequestsBypassCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	db := "file:toolcache?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := NewRouter(s)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]}`
	send := func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
		}
	}
	send()
	send()

	if calls != 2 {
		t.Fatalf("tool requests must bypass cache, upstream calls=%d", calls)
	}
	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	recs, err := repo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, rec := range recs {
		if rec.CacheHit {
			t.Fatalf("unexpected cache hit for tool request: %+v", rec)
		}
	}
}
