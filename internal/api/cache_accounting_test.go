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

func TestChatCacheHitUsageTotalTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"deepseek-chat","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`)
	}))
	defer upstream.Close()

	db := "file:cache_total_tokens?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "deepseek-chat"}},
	}
	r := NewRouter(s)

	send := func() map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("invalid JSON body: %v; body=%q", err, w.Body.String())
		}
		return payload
	}

	send()
	hit := send()
	usageObj, ok := hit["usage"].(map[string]any)
	if !ok {
		t.Fatalf("missing usage object, body=%+v", hit)
	}
	prompt, _ := usageObj["prompt_tokens"].(float64)
	completion, _ := usageObj["completion_tokens"].(float64)
	total, _ := usageObj["total_tokens"].(float64)
	if total <= 0 {
		t.Fatalf("cache hit usage.total_tokens must be > 0, got %v", total)
	}
	if total != prompt+completion {
		t.Fatalf("usage.total_tokens (%v) != prompt_tokens (%v) + completion_tokens (%v)", total, prompt, completion)
	}
}

func TestChatCacheHitIsRecordedWithSavedCost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"deepseek-chat","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":1000,"total_tokens":2000}}`)
	}))
	defer upstream.Close()

	db := "file:cache_accounting?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "deepseek-chat"}},
	}
	r := NewRouter(s)

	send := func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
		}
	}
	send()
	send()

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()

	recs, err := repo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var hit *usage.Record
	for i := range recs {
		if recs[i].CacheHit {
			hit = &recs[i]
		}
	}
	if hit == nil {
		t.Fatalf("want a cache_hit record, got %+v", recs)
	}
	if hit.SavedCost <= 0 {
		t.Fatalf("want saved_cost > 0, got %v", hit.SavedCost)
	}
	if hit.Cost != 0 {
		t.Fatalf("cache hit must not add to cost, got %v", hit.Cost)
	}
	if saved, _ := repo.MonthlySaved(context.Background(), usage.CurrentMonth()); saved <= 0 {
		t.Fatalf("want month saved > 0, got %v", saved)
	}
}
