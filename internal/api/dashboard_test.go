package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDashboardServesHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "LLM Gateway") {
		t.Fatalf("want dashboard html, got %d", w.Code)
	}
}