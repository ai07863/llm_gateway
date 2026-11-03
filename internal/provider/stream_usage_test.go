package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-gateway/internal/config"
)

func TestOpenAICompatChatStreamCapturesUsage(t *testing.T) {
	var gotStreamOptions map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStreamOptions, _ = body["stream_options"].(map[string]any)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, tok := range []string{"He", "llo"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tok)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":5,\"total_tokens\":12}}\n\n")
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
	var terminal StreamChunk
	for c := range ch {
		if c.Done {
			terminal = c
			continue
		}
		sb.WriteString(c.Content)
	}
	if sb.String() != "Hello" {
		t.Fatalf("want 'Hello', got %q", sb.String())
	}
	if terminal.Err != nil {
		t.Fatalf("unexpected stream error: %v", terminal.Err)
	}
	if terminal.PromptTokens != 7 || terminal.CompletionTokens != 5 || terminal.TotalTokens != 12 {
		t.Fatalf("want usage 7/5/12, got %d/%d/%d", terminal.PromptTokens, terminal.CompletionTokens, terminal.TotalTokens)
	}
	if gotStreamOptions == nil || gotStreamOptions["include_usage"] != true {
		t.Fatalf("want stream_options.include_usage=true sent upstream, got %#v", gotStreamOptions)
	}
}

func TestOpenAICompatChatStreamEmitsErrorOnMissingDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"})
	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var terminal StreamChunk
	for c := range ch {
		if c.Done {
			terminal = c
		}
	}
	if terminal.Err == nil {
		t.Fatal("want terminal error chunk for stream without [DONE], got nil")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errAfterReader struct {
	data []byte
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if len(e.data) > 0 {
		n := copy(p, e.data)
		e.data = e.data[n:]
		return n, nil
	}
	return 0, errors.New("boom read")
}

func (e *errAfterReader) Close() error { return nil }

func TestOpenAICompatChatStreamEmitsErrorOnScannerFailure(t *testing.T) {
	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: "http://fake", APIKey: "k", Model: "m"}).(*openAICompat)
	p.streamClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &errAfterReader{data: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")},
		}, nil
	})}

	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var terminal StreamChunk
	for c := range ch {
		if c.Done {
			terminal = c
		}
	}
	if terminal.Err == nil {
		t.Fatal("want terminal error chunk when the upstream body read fails, got nil")
	}
}

var _ io.Closer = (*errAfterReader)(nil)

func TestNewOpenAICompatStreamClientHasNoOverallTimeout(t *testing.T) {
	p := NewOpenAICompat(config.ProviderConfig{Name: "fake"}).(*openAICompat)
	if p.client.Timeout != 60*time.Second {
		t.Fatalf("non-stream client want 60s timeout, got %v", p.client.Timeout)
	}
	if p.streamClient.Timeout != 0 {
		t.Fatalf("stream client must have no overall timeout, got %v", p.streamClient.Timeout)
	}
}

func TestOpenAICompatChatStreamNotBoundedByClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(*openAICompat)
	// Short analogue of the 60s non-stream timeout: if ChatStream used p.client,
	// the stream would be truncated ~50ms in (before the second chunk).
	p.client.Timeout = 50 * time.Millisecond

	ch, err := p.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	var sb strings.Builder
	var terminal StreamChunk
	for c := range ch {
		if c.Done {
			terminal = c
			continue
		}
		sb.WriteString(c.Content)
	}
	if terminal.Err != nil {
		t.Fatalf("long stream must not be cut by the non-stream client timeout, got error: %v", terminal.Err)
	}
	if sb.String() != "firstsecond" {
		t.Fatalf("want 'firstsecond', got %q", sb.String())
	}
}
