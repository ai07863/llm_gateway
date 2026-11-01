package metrics

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestSnapshotCountsAndErrorRate(t *testing.T) {
	reg := New()
	reg.Requests.WithLabelValues("p1", "200").Add(8)
	reg.Requests.WithLabelValues("p1", "429").Add(2)

	s := reg.Snapshot()
	if s.RequestsTotal != 10 {
		t.Fatalf("requests total: %d", s.RequestsTotal)
	}
	if math.Abs(s.ErrorRate-0.2) > 1e-9 {
		t.Fatalf("error rate: %v", s.ErrorRate)
	}
	if len(s.ByStatus) != 2 {
		t.Fatalf("by_status: %+v", s.ByStatus)
	}
}

func TestSnapshotLatencyPercentiles(t *testing.T) {
	reg := New()
	for _, v := range []float64{0.1, 0.1, 0.2, 0.2, 5.0} {
		reg.Latency.WithLabelValues("p1").Observe(v)
	}

	s := reg.Snapshot()
	if s.LatencyMS.Avg < 1000 || s.LatencyMS.Avg > 1200 {
		t.Fatalf("avg: %v", s.LatencyMS.Avg)
	}
	if s.LatencyMS.P50 <= 0 || s.LatencyMS.P50 >= s.LatencyMS.P95 {
		t.Fatalf("percentiles: %+v", s.LatencyMS)
	}
}

func TestSnapshotKeepsLongLatencies(t *testing.T) {
	reg := New()
	reg.Latency.WithLabelValues("p1").Observe(20)

	s := reg.Snapshot()
	if s.LatencyMS.P95 <= 10000 {
		t.Fatalf("p95 clamped at 10s, want >10000ms: %+v", s.LatencyMS)
	}
}

func TestSnapshotReportsUptime(t *testing.T) {
	reg := New()
	s := reg.Snapshot()
	if s.StartedAt.IsZero() {
		t.Fatal("started_at must be set")
	}
	if s.UptimeSeconds < 0 {
		t.Fatalf("uptime_seconds must be non-negative: %d", s.UptimeSeconds)
	}
	if time.Since(s.StartedAt) < time.Duration(s.UptimeSeconds)*time.Second-2*time.Second {
		t.Fatalf("started_at/uptime inconsistent: %v / %d", s.StartedAt, s.UptimeSeconds)
	}
}

func TestSnapshotEmpty(t *testing.T) {
	s := New().Snapshot()
	if s.RequestsTotal != 0 || s.ErrorRate != 0 || s.LatencyMS.P95 != 0 {
		t.Fatalf("empty snapshot should be zero: %+v", s)
	}
	if s.ByStatus == nil {
		t.Fatal("by_status must be an empty slice, not null")
	}
}
