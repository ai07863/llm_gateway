package proxy

import (
	"context"
	"errors"
	"testing"

	"llm-gateway/internal/provider"
)

type responseProvider struct {
	result provider.ResponsesResult
	err    error
	calls  int
	last   provider.ResponsesRequest
}

func (f *responseProvider) Name() string      { return "fake" }
func (f *responseProvider) ChatModel() string { return "fake-model" }
func (f *responseProvider) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{}, errors.New("not implemented")
}
func (f *responseProvider) ChatStream(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamChunk, error) {
	return nil, errors.New("not implemented")
}
func (f *responseProvider) Responses(ctx context.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return provider.ResponsesResult{}, f.err
	}
	res := f.result
	if res.Model == "" {
		res.Model = req.Model
	}
	return res, nil
}
func (f *responseProvider) ResponsesStream(ctx context.Context, req provider.ResponsesRequest) (<-chan provider.ResponsesChunk, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan provider.ResponsesChunk, 2)
	ch <- provider.ResponsesChunk{Raw: "data: ok\n\n"}
	ch <- provider.ResponsesChunk{Done: true, TotalTokens: 7}
	close(ch)
	return ch, nil
}

func TestResponsesWithFailoverRetriesThenSucceeds(t *testing.T) {
	fastRetry(t)
	old := MaxAttempts
	MaxAttempts = 1
	t.Cleanup(func() { MaxAttempts = old })

	bad := &responseProvider{err: provider.NewStatusError("bad", 500, "boom")}
	good := &responseProvider{result: provider.ResponsesResult{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7, Raw: []byte(`{"id":"ok"}`)}}
	targets := []Target{{Provider: bad, Model: "primary-m"}, {Provider: good, Model: "backup-m"}}

	res, used, err := ResponsesWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "primary-m", Raw: []byte(`{"model":"primary-m"}`)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bad.calls != 1 || good.calls != 1 {
		t.Fatalf("want one call each, got bad=%d good=%d", bad.calls, good.calls)
	}
	if used.Model != "backup-m" || good.last.Model != "backup-m" {
		t.Fatalf("want backup target, got used=%s req=%s", used.Model, good.last.Model)
	}
	if string(good.last.Raw) != `{"model":"primary-m"}` {
		t.Fatalf("raw body must not be mutated, got %s", good.last.Raw)
	}
	if res.PromptTokens != 3 || res.CompletionTokens != 4 || res.TotalTokens != 7 || string(res.Raw) != `{"id":"ok"}` {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestResponsesWithFailoverNonRetryableStopsImmediately(t *testing.T) {
	fastRetry(t)
	primary := &responseProvider{err: provider.NewStatusError("primary", 400, "bad")}
	backup := &responseProvider{}
	targets := []Target{{Provider: primary, Model: "m"}, {Provider: backup, Model: "b"}}

	if _, _, err := ResponsesWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "m"}); err == nil {
		t.Fatal("want non-retryable error")
	}
	if primary.calls != 1 || backup.calls != 0 {
		t.Fatalf("non-retryable must not retry or fail over: primary=%d backup=%d", primary.calls, backup.calls)
	}
}

func TestResponsesStreamWithFailoverRetriesBeforeFirstChunk(t *testing.T) {
	fastRetry(t)
	old := MaxAttempts
	MaxAttempts = 1
	t.Cleanup(func() { MaxAttempts = old })

	bad := &responseProvider{err: provider.NewStatusError("bad", 503, "unavailable")}
	good := &responseProvider{}
	targets := []Target{{Provider: bad, Model: "primary-m"}, {Provider: good, Model: "backup-m"}}

	ch, used, err := ResponsesStreamWithFailover(context.Background(), targets, provider.ResponsesRequest{Model: "primary-m", Stream: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw string
	var done provider.ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
			continue
		}
		raw += c.Raw
	}
	if used.Model != "backup-m" || raw != "data: ok\n\n" || done.TotalTokens != 7 {
		t.Fatalf("unexpected stream: used=%s raw=%q done=%+v", used.Model, raw, done)
	}
}

func TestResponsesFailoverEmptyTargetsReturnsError(t *testing.T) {
	if _, _, err := ResponsesWithFailover(context.Background(), nil, provider.ResponsesRequest{}); err == nil {
		t.Fatal("want error for empty targets")
	}
	if _, _, err := ResponsesStreamWithFailover(context.Background(), nil, provider.ResponsesRequest{}); err == nil {
		t.Fatal("want error for empty targets")
	}
}
