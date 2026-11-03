package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

func readDataLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- ""
				return
			}
			if strings.HasPrefix(line, "data:") {
				ch <- line
				return
			}
		}
	}()
	select {
	case line := <-ch:
		if line == "" {
			t.Fatal("stream closed before a data line")
		}
		return line
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE data line")
		return ""
	}
}

func TestUsageHubPublishSubscribe(t *testing.T) {
	h := newUsageHub()
	ch := h.subscribe()
	defer h.unsubscribe(ch)

	go h.publish(usage.Record{ID: "r1", Model: "m"})

	select {
	case rec := <-ch:
		if rec.ID != "r1" {
			t.Fatalf("want r1, got %+v", rec)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published record")
	}
}

func TestUsageHubDropsWhenSubscriberFull(t *testing.T) {
	h := newUsageHub()
	ch := h.subscribe()
	defer h.unsubscribe(ch)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			h.publish(usage.Record{ID: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a full subscriber")
	}
}

func TestUsageStreamEmitsRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	hub := newUsageHub()
	registerUsageStream(v1, hub)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/admin/usage/stream")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want event-stream, got %q", ct)
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		hub.publish(usage.Record{ID: "streamed", Model: "deepseek-v4.1-flash"})
	}()

	line := readDataLine(t, bufio.NewReader(resp.Body))
	if !strings.Contains(line, "streamed") {
		t.Fatalf("unexpected SSE line: %q", line)
	}
}

func TestRouterStreamsUsageAfterChat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer upstream.Close()

	s := config.Settings{
		GatewayToken: "tok", DBPath: "file:streamwire?mode=memory&cache=shared", BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := NewRouter(s)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// The handler flushes ": connected" immediately, so Do returns at once.
	reqStream, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/admin/usage/stream", nil)
	reqStream.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(reqStream)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	chatBody := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	reqChat, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(chatBody))
	reqChat.Header.Set("Authorization", "Bearer tok")
	reqChat.Header.Set("Content-Type", "application/json")
	if _, err := http.DefaultClient.Do(reqChat); err != nil {
		t.Fatalf("chat: %v", err)
	}

	line := readDataLine(t, bufio.NewReader(resp.Body))
	if !strings.Contains(line, `"model":"m"`) || !strings.Contains(line, `"provider":"up"`) {
		t.Fatalf("streamed event missing model/provider: %q", line)
	}
}

func TestUsageRecordJSONShape(t *testing.T) {
	b, _ := json.Marshal(usage.Record{ID: "x", Model: "m", Provider: "p", Cost: 0.1, SavedCost: 0.2, CacheHit: true})
	s := string(b)
	for _, want := range []string{`"model":"m"`, `"provider":"p"`, `"saved_cost":0.2`, `"cache_hit":true`} {
		if !strings.Contains(s, want) {
			t.Fatalf("record json missing %s: %s", want, s)
		}
	}
}
