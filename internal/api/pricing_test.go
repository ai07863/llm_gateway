package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func TestAdminPricingReturnsTable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := config.Settings{GatewayToken: "tok", DBPath: "file:admin_pricing?mode=memory&cache=shared"}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/pricing", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	models, ok := payload["models"].(map[string]any)
	if !ok {
		t.Fatalf("missing models, body=%q", w.Body.String())
	}
	if _, ok := models["deepseek-v4.1-flash"]; !ok {
		t.Fatalf("expected deepseek-v4.1-flash in pricing, got %v", models)
	}
	if payload["source"] == "" {
		t.Fatal("expected a source field")
	}
}
