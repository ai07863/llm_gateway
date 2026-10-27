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

func TestDashboardUsesPreciseCostFormatting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if strings.Contains(body, "toFixed(3)") {
		t.Fatal("dashboard must not round cost/saved to 3 decimals: sub-cent spend shows as 0")
	}
}

func TestDashboardRemembersToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(w.Body.String(), "localStorage") {
		t.Fatal("dashboard should persist the gateway token so the user does not retype it every visit")
	}
}

func TestDashboardShowsModelPerRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, `id="records"`) || !strings.Contains(body, "r.model") {
		t.Fatal("dashboard should render a per-call table including the model name")
	}
}

func TestDashboardAutoRefreshes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(w.Body.String(), "setInterval") {
		t.Fatal("dashboard should poll so the numbers update while the user keeps calling the gateway")
	}
}

func TestDashboardSupportsTokenQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerDashboard(r)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "URLSearchParams") || !strings.Contains(body, "replaceState") {
		t.Fatal("dashboard should accept a ?token= URL param and strip it, so a bookmarked URL never prompts again")
	}
}
