package tests

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func jsonBody(s string) *strings.Reader { return strings.NewReader(s) }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
