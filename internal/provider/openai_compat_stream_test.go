package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-gateway/internal/config"
)

type closeRecorderTransport struct {
	base     http.RoundTripper
	closeSig chan struct{}
}

func (t *closeRecorderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	res.Body = &closeRecorderBody{ReadCloser: res.Body, closeSig: t.closeSig}
	return res, nil
}

type closeRecorderBody struct {
	io.ReadCloser
	closeSig chan struct{}
	once     sync.Once
}

func (b *closeRecorderBody) Close() error {
	b.once.Do(func() { close(b.closeSig) })
	return b.ReadCloser.Close()
}

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

func TestOpenAICompatChatStreamClosesOnContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, tok := range []string{"a", "b", "c"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tok)
			flusher.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)

	bodyClosed := make(chan struct{})
	p := NewOpenAICompat(config.ProviderConfig{Name: "fake", BaseURL: srv.URL, APIKey: "k", Model: "m"}).(*openAICompat)
	p.client = &http.Client{Transport: &closeRecorderTransport{base: http.DefaultTransport, closeSig: bodyClosed}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.ChatStream(ctx, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-bodyClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream body not closed after context cancel: goroutine leaked blocked on send")
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected chunk after cancellation; want closed channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after context cancellation")
	}
}
