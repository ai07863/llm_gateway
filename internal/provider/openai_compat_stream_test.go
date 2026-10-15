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
