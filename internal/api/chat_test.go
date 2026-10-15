package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

func TestRegisterChatErrorWritesSingleJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	registerChat(v1, func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("boom")
	})

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
	registerChat(v1, func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Model: "m", Content: "hi there", PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, nil
	})

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