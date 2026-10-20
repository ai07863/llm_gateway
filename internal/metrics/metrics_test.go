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
	out := w.Body.String()
	if !strings.Contains(out, "gateway_requests_total") {
		t.Fatalf("metrics output missing counter: %s", out)
	}
	if !strings.Contains(out, `gateway_requests_total{provider="deepseek",status="200"} 1`) {
		t.Fatalf("counter value not incremented to 1: %s", out)
	}
}

func TestMetricsEndpointExposesLatencyHistogram(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := New()
	reg.Latency.WithLabelValues("deepseek").Observe(0.1)

	r := gin.New()
	r.GET("/metrics", reg.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := w.Body.String()
	if !strings.Contains(out, "gateway_request_duration_seconds") {
		t.Fatalf("metrics output missing latency histogram: %s", out)
	}
	if !strings.Contains(out, `provider="deepseek"`) {
		t.Fatalf("latency histogram missing provider label: %s", out)
	}
}
