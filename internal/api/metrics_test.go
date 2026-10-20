package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func TestChatSuccessRecordsMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-4o-mini","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()

	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             "file:metrics_chat?mode=memory&cache=shared",
		BudgetMonthlyLimit: 100,
		Providers:          []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"}},
	}
	r := NewRouter(s)

	body := strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%q", w.Code, w.Body.String())
	}

	mw := httptest.NewRecorder()
	r.ServeHTTP(mw, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := mw.Body.String()

	if !strings.Contains(out, `gateway_requests_total{provider="up",status="200"} 1`) {
		t.Fatalf("requests counter not recorded: %s", out)
	}
	if !strings.Contains(out, `gateway_tokens_total{kind="prompt",provider="up"} 3`) {
		t.Fatalf("prompt tokens not recorded: %s", out)
	}
	if !strings.Contains(out, `gateway_tokens_total{kind="completion",provider="up"} 2`) {
		t.Fatalf("completion tokens not recorded: %s", out)
	}
	if !strings.Contains(out, "gateway_request_duration_seconds") || !strings.Contains(out, `provider="up"`) {
		t.Fatalf("latency histogram not observed for provider: %s", out)
	}
}
