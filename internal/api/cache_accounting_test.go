package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

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
