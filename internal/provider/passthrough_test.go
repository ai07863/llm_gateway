package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-gateway/internal/config"
)

func TestChatForwardsRawBodyAndReturnsRaw(t *testing.T) {
	var gotTools bool
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, gotTools = body["tools"]
		_ = json.Unmarshal(body["model"], &gotModel)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"target-model","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	raw := json.RawMessage(`{"model":"orig","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]}`)
	resp, err := p.Chat(context.Background(), ChatRequest{Model: "target-model", Raw: raw})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !gotTools {
		t.Fatal("upstream did not receive tools")
	}
	if gotModel != "target-model" {
		t.Fatalf("model not rewritten: %q", gotModel)
	}
	if !strings.Contains(string(resp.Raw), "tool_calls") {
		t.Fatalf("raw response lost tool_calls: %s", resp.Raw)
	}
	if resp.PromptTokens != 5 || resp.CompletionTokens != 2 {
		t.Fatalf("usage not parsed: %+v", resp)
	}
}

func TestChatStreamForwardsRawToolCallDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Raw: json.RawMessage(`{"model":"m","stream":true,"messages":[]}`)})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var sawTool, terminal StreamChunk
	for c := range ch {
		if c.Raw != "" && strings.Contains(c.Raw, "tool_calls") {
			sawTool = c
		}
		if c.Done {
			terminal = c
		}
	}
	if sawTool.Raw == "" {
		t.Fatal("tool_call delta was not forwarded in Raw")
	}
	if terminal.PromptTokens != 5 || terminal.CompletionTokens != 2 {
		t.Fatalf("usage not captured: %+v", terminal)
	}
}
