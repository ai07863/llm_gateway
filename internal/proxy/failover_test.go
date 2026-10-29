package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

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

type scriptedProvider struct {
	name    string
	content string
	errs    []error
	calls   int
}

func (p *scriptedProvider) Name() string { return p.name }
func (p *scriptedProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	i := p.calls
	p.calls++
	if i < len(p.errs) && p.errs[i] != nil {
		return provider.ChatResponse{}, p.errs[i]
	}
	return provider.ChatResponse{Content: p.content}, nil
}
func (p *scriptedProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	i := p.calls
	p.calls++
	if i < len(p.errs) && p.errs[i] != nil {
		return nil, p.errs[i]
	}
	ch := make(chan provider.StreamChunk, 1)
	ch <- provider.StreamChunk{Content: p.content}
	close(ch)
	return ch, nil
}

func fastRetry(t *testing.T) {
	t.Helper()
	oldMax, oldBackoff := MaxAttempts, RetryBackoff
	MaxAttempts, RetryBackoff = 3, time.Millisecond
	t.Cleanup(func() { MaxAttempts, RetryBackoff = oldMax, oldBackoff })
}

func TestChatWithFailoverUsesBackup(t *testing.T) {
	fastRetry(t)
	targets := []Target{
		{Provider: fakeProvider{name: "primary", fail: true}, Model: "m"},
		{Provider: fakeProvider{name: "backup", content: "ok"}, Model: "b"},
	}
	resp, used, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if resp.Content != "ok" || used.Name() != "backup" {
		t.Fatalf("want backup/ok, got %s/%s", used.Name(), resp.Content)
	}
}

func TestChatWithFailoverRetriesRetryableThenSucceeds(t *testing.T) {
	fastRetry(t)
	p := &scriptedProvider{name: "flaky", content: "ok", errs: []error{
		provider.NewStatusError("flaky", 503, "boom"),
		nil,
	}}
	targets := []Target{{Provider: p, Model: "m"}}
	resp, _, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("want success after retry, got %v", err)
	}
	if resp.Content != "ok" || p.calls != 2 {
		t.Fatalf("want 2 calls and 'ok', got calls=%d content=%q", p.calls, resp.Content)
	}
}

func TestChatWithFailoverNonRetryableStopsImmediately(t *testing.T) {
	fastRetry(t)
	primary := &scriptedProvider{name: "primary", errs: []error{provider.NewStatusError("primary", 400, "bad")}}
	backup := &scriptedProvider{name: "backup", content: "ok"}
	targets := []Target{{Provider: primary, Model: "m"}, {Provider: backup, Model: "b"}}
	_, _, err := ChatWithFailover(context.Background(), targets, provider.ChatRequest{})
	if err == nil {
		t.Fatal("want error for non-retryable failure")
	}
	if primary.calls != 1 {
		t.Fatalf("non-retryable must not retry, got %d calls", primary.calls)
	}
	if backup.calls != 0 {
		t.Fatalf("non-retryable must not fail over, backup calls=%d", backup.calls)
	}
}

func TestFailoverEmptyProvidersReturnsError(t *testing.T) {
	resp, used, err := ChatWithFailover(context.Background(), nil, provider.ChatRequest{})
	if err == nil {
		t.Fatalf("ChatWithFailover: want error, got nil (resp=%+v)", resp)
	}
	if used != nil {
		t.Fatalf("ChatWithFailover: want nil provider, got %v", used)
	}

	ch, usedStream, err := StreamWithFailover(context.Background(), nil, provider.ChatRequest{})
	if err == nil {
		t.Fatalf("StreamWithFailover: want error, got nil (ch=%v)", ch)
	}
	if usedStream != nil {
		t.Fatalf("StreamWithFailover: want nil provider, got %v", usedStream)
	}
}
