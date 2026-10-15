package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMetricsEndpointExposesCounters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := New()
	reg.Requests.WithLabelValues("deepseek", "200").Inc()

	r := gin.New()
	r.GET("/metrics", reg.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "gateway_requests_total") {
		t.Fatalf("metrics output missing counter: %s", w.Body.String())
	}
}
