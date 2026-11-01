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

func TestModelsListsAdditionalModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	s := config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:models-extra?mode=memory&cache=shared",
		Providers: []config.ProviderConfig{{
			Name: "dashscope", BaseURL: "http://example.invalid", APIKey: "k",
			Model:  "deepseek-v4.1-flash",
			Models: []string{"deepseek-v4.1-flash", "deepseek-v4.1-flash-1017"},
		}},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, m := range payload.Data {
		got[m.ID] = true
	}
	for _, want := range []string{"deepseek-v4.1-flash", "deepseek-v4.1-flash-1017"} {
		if !got[want] {
			t.Fatalf("/v1/models missing %q, got %+v", want, payload.Data)
		}
	}
}

func TestChatRoutesAdditionalModelToOwningProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var primaryCalled bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalled = true
		_, _ = io.WriteString(w, `{"model":"primary-model","choices":[{"message":{"role":"assistant","content":"wrong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer primary.Close()

	var gotModel string
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"model":"`+gotModel+`","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer secondary.Close()

	s := config.Settings{
		GatewayToken: "tok",
		DBPath:       "file:models-routing?mode=memory&cache=shared",
		Providers: []config.ProviderConfig{
			{Name: "primary", BaseURL: primary.URL, APIKey: "k", Model: "primary-model", Models: []string{"primary-model"}},
			{Name: "secondary", BaseURL: secondary.URL, APIKey: "k", Model: "secondary-model",
				Models: []string{"secondary-model", "secondary-extra"}},
		},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"secondary-extra","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	if primaryCalled {
		t.Fatal("additional model must not be sent to the first provider")
	}
	if gotModel != "secondary-extra" {
		t.Fatalf("owner must receive the requested model, got %q", gotModel)
	}
}
