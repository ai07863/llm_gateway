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

func TestChatBudgetRejectReturns429SingleJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := "file:budget_reject?mode=memory&cache=shared"
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

	body := strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
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

func TestChatBudgetDegradeSwapsModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			gotModel, _ = req["model"].(string)
			_, _ = io.WriteString(w, `{"model":"`+gotModel+`","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"embedding":[1,0]}]}`)
	}))
	defer upstream.Close()

	db := "file:budget_degrade?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken:       "tok",
		DBPath:             db,
		BudgetMonthlyLimit: 10,
		BudgetSoftRatio:    0.5,
		Providers: []config.ProviderConfig{
			{Name: "deepseek", BaseURL: upstream.URL, APIKey: "k", Model: "deepseek-chat"},
			{Name: "openai", BaseURL: upstream.URL, APIKey: "k", Model: "gpt-4o-mini"},
		},
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

	body := strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body=%q", w.Code, w.Body.String())
	}
	if gotModel != "deepseek-chat" {
		t.Fatalf("want degraded model deepseek-chat upstream, got %q", gotModel)
	}
}
