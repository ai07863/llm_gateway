package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-gateway/internal/config"
)

type responsesCaller interface {
	Responses(context.Context, ResponsesRequest) (ResponsesResult, error)
}

func TestOpenAICompatResponsesRewritesModelAndParsesUsage(t *testing.T) {
	const upstreamBody = `{"id":"resp_1","object":"response","model":"m2","output":[],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m1"}).(responsesCaller)
	res, err := p.Responses(context.Background(), ResponsesRequest{
		Model: "m2",
		Raw:   []byte(`{"model":"m1","input":"hi","custom":{"keep":true}}`),
	})
	if err != nil {
		t.Fatalf("responses error: %v", err)
	}
	if string(got["model"]) != `"m2"` {
		t.Fatalf("upstream model must be rewritten to m2, got %s", got["model"])
	}
	if string(got["custom"]) != `{"keep":true}` {
		t.Fatalf("unknown field must survive, got %s", got["custom"])
	}
	if res.PromptTokens != 11 || res.CompletionTokens != 7 || res.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", res)
	}
	if string(res.Raw) != upstreamBody {
		t.Fatalf("raw body must pass through unchanged, got %q", res.Raw)
	}
}

func TestOpenAICompatResponses4xxExposesBodyAndIsNotRetryable(t *testing.T) {
	const errBody = `{"error":{"message":"bad input"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, errBody)
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(responsesCaller)
	_, err := p.Responses(context.Background(), ResponsesRequest{Model: "m", Raw: []byte(`{"model":"m","input":"hi"}`)})
	if err == nil {
		t.Fatal("want error")
	}
	if IsRetryable(err) {
		t.Fatalf("4xx must not be retryable: %v", err)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *provider.Error, got %T", err)
	}
	if pe.StatusCode != http.StatusBadRequest || pe.Body != errBody {
		t.Fatalf("want status 400 and upstream body, got %d %q", pe.StatusCode, pe.Body)
	}
}

func TestOpenAICompatResponsesStreamForwardsFramesAndParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if string(body["model"]) != `"m2"` {
			t.Errorf("model not rewritten: %s", body["model"])
		}
		if string(body["stream"]) != "true" {
			t.Errorf("stream not forced: %s", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id:1\nevent:response.created\n:HTTP_STATUS/200\ndata:{\"type\":\"response.created\"}\n\n")
		_, _ = io.WriteString(w, "id:2\nevent:response.completed\n:HTTP_STATUS/200\ndata:{\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2,\"total_tokens\":7}}}\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m1"}).(ResponsesProvider)
	ch, err := p.ResponsesStream(context.Background(), ResponsesRequest{Model: "m2", Stream: true, Raw: []byte(`{"model":"m1","input":"hi"}`)})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	var raw strings.Builder
	var done ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
			break
		}
		raw.WriteString(c.Raw)
	}
	if !strings.Contains(raw.String(), "event:response.created") || !strings.Contains(raw.String(), ":HTTP_STATUS/200") {
		t.Fatalf("frames must be forwarded verbatim, got %q", raw.String())
	}
	if done.Err != nil {
		t.Fatalf("unexpected done error: %v", done.Err)
	}
	if done.PromptTokens != 5 || done.CompletionTokens != 2 || done.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", done)
	}
}

func TestOpenAICompatResponsesStreamEarlyEOFIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id:1\nevent:response.created\ndata:{\"type\":\"response.created\"}\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(ResponsesProvider)
	ch, err := p.ResponsesStream(context.Background(), ResponsesRequest{Model: "m", Stream: true, Raw: []byte(`{"model":"m","input":"hi"}`)})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	var done ResponsesChunk
	for c := range ch {
		if c.Done {
			done = c
		}
	}
	if done.Err == nil {
		t.Fatal("want error when upstream closes before response.completed")
	}
}
