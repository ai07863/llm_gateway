package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type closeNotifierRecorder struct {
	*httptest.ResponseRecorder
	closeCh chan bool
}

func newCloseNotifierRecorder() *closeNotifierRecorder {
	return &closeNotifierRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeCh:          make(chan bool),
	}
}

func (r *closeNotifierRecorder) CloseNotify() <-chan bool { return r.closeCh }

func TestRegisterChatErrorWritesSingleJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
			return provider.ChatResponse{}, errors.New("boom")
		},
		func(c *gin.Context, req provider.ChatRequest) error { return nil },
	)

	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not a single valid JSON document: %v; body=%q", err, w.Body.String())
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object, body=%q", w.Body.String())
	}
	if errObj["message"] != "boom" {
		t.Fatalf("want message 'boom', got %v", errObj["message"])
	}
}

func TestRegisterChatSuccessWritesSingleJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
			return provider.ChatResponse{Model: "m", Content: "hi there", PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, nil
		},
		func(c *gin.Context, req provider.ChatRequest) error { return nil },
	)

	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not a single valid JSON document: %v; body=%q", err, w.Body.String())
	}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("want 1 choice, body=%q", w.Body.String())
	}
}

func TestRegisterChatStreamDispatchesToStreamCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	nonStreamCalled := false
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
			nonStreamCalled = true
			return provider.ChatResponse{}, nil
		},
		func(c *gin.Context, req provider.ChatRequest) error {
			c.Header("Content-Type", "text/event-stream")
			c.Stream(func(w io.Writer) bool {
				fmt.Fprint(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
				return false
			})
			return nil
		},
	)

	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	w := newCloseNotifierRecorder()
	r.ServeHTTP(w, req)

	if nonStreamCalled {
		t.Fatal("non-stream chat callback must not be called when stream=true")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want text/event-stream, got %q", ct)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("want SSE done marker, body=%q", w.Body.String())
	}
}

func TestRegisterChatStreamErrorWritesSingleJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
			return provider.ChatResponse{}, errors.New("should not be called")
		},
		func(c *gin.Context, req provider.ChatRequest) error {
			return errors.New("stream boom")
		},
	)

	body := strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not a single valid JSON document: %v; body=%q", err, w.Body.String())
	}
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object, body=%q", w.Body.String())
	}
	if errObj["message"] != "stream boom" {
		t.Fatalf("want message 'stream boom', got %v", errObj["message"])
	}
}
