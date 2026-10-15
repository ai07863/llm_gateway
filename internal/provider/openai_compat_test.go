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