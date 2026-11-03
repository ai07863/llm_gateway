package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func embeddingsUpstream(t *testing.T, gotInput *[]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if arr, ok := body["input"].([]any); ok {
			*gotInput = arr
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"embedding":[0.1,0.2]},{"embedding":[0.3,0.4]}]}`)
	}))
}

func TestEmbeddingsReturnsOpenAIStyleList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotInput []any
	upstream := embeddingsUpstream(t, &gotInput)
	defer upstream.Close()

	s := config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:embeddings?mode=memory&cache=shared",
		Providers: []config.ProviderConfig{{
			Name: "up", BaseURL: upstream.URL, APIKey: "k",
			Model: "gpt-4o-mini", EmbedModel: "text-embedding-3-small",
		}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":["a","b"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	var payload struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v; body=%q", err, w.Body.String())
	}
	if payload.Object != "list" {
		t.Fatalf("want object 'list', got %q", payload.Object)
	}
	if payload.Model != "text-embedding-3-small" {
		t.Fatalf("want model echoed, got %q", payload.Model)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("want 2 embeddings, got %d", len(payload.Data))
	}
	if payload.Data[0].Object != "embedding" || payload.Data[0].Index != 0 || len(payload.Data[0].Embedding) != 2 {
		t.Fatalf("unexpected first embedding: %+v", payload.Data[0])
	}
	if payload.Data[1].Index != 1 {
		t.Fatalf("want index 1 on second embedding, got %d", payload.Data[1].Index)
	}
	if len(gotInput) != 2 {
		t.Fatalf("upstream should receive 2 inputs, got %d", len(gotInput))
	}
}

func TestEmbeddingsAcceptsStringInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotInput []any
	upstream := embeddingsUpstream(t, &gotInput)
	defer upstream.Close()

	s := config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:embeddings_str?mode=memory&cache=shared",
		Providers: []config.ProviderConfig{{
			Name: "up", BaseURL: upstream.URL, APIKey: "k",
			Model: "gpt-4o-mini", EmbedModel: "text-embedding-3-small",
		}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	if len(gotInput) != 1 || gotInput[0] != "hello" {
		t.Fatalf("want upstream to receive [\"hello\"], got %#v", gotInput)
	}
}

func TestEmbeddingsBadInputReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := config.Settings{GatewayToken: "tok", DBPath: "file:embeddings_bad?mode=memory&cache=shared"}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":42}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestEmbeddingsUnsupportedProviderReturns502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	s := config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:embeddings_unsupported?mode=memory&cache=shared",
		Providers:    []config.ProviderConfig{{Name: "up", BaseURL: upstream.URL, APIKey: "k", Model: "m"}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502 (no embed model configured), got %d body=%q", w.Code, w.Body.String())
	}
}

func TestEmbeddingsRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := config.Settings{GatewayToken: "tok", DBPath: "file:embeddings_auth?mode=memory&cache=shared"}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%q", w.Code, w.Body.String())
	}
}
