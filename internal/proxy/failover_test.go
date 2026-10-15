package proxy

import (
	"context"
	"errors"
	"testing"

	"llm-gateway/internal/provider"
)

type fakeProvider struct {
	name    string
	fail    bool
	content string
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if f.fail {
		return provider.ChatResponse{}, errors.New("boom")
	}
	return provider.ChatResponse{Content: f.content, TotalTokens: 1}, nil
}
func (f fakeProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	if f.fail {
		return nil, errors.New("boom")
	}
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: f.content}
	close(ch)
	return ch, nil
}

func TestChatWithFailoverUsesBackup(t *testing.T) {
	ps := []provider.Provider{
		fakeProvider{name: "primary", fail: true},
		fakeProvider{name: "backup", content: "ok"},
	}
	resp, used, err := ChatWithFailover(context.Background(), ps, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if resp.Content != "ok" || used.Name() != "backup" {
		t.Fatalf("want backup/ok, got %s/%s", used.Name(), resp.Content)
	}
}