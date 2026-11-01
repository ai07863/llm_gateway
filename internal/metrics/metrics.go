package metrics

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var latencyBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120}

type Registry struct {
	reg       *prometheus.Registry
	startedAt time.Time
	Requests  *prometheus.CounterVec
	Latency   *prometheus.HistogramVec
	Tokens    *prometheus.CounterVec
}

func New() *Registry {
	reg := prometheus.NewRegistry()
	m := &Registry{
		reg:       reg,
		startedAt: time.Now(),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total", Help: "total requests",
		}, []string{"provider", "status"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gateway_request_duration_seconds", Help: "request latency",
			Buckets: latencyBuckets,
		}, []string{"provider"}),
		Tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_tokens_total", Help: "tokens used",
		}, []string{"provider", "kind"}),
	}
	reg.MustRegister(m.Requests, m.Latency, m.Tokens)
	return m
}

func (m *Registry) Handler() gin.HandlerFunc {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	return func(c *gin.Context) { h.ServeHTTP(c.Writer, c.Request) }
}

type StatusCount struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Count    int    `json:"count"`
}

type LatencyMS struct {
	Avg float64 `json:"avg"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

type Snapshot struct {
	RequestsTotal int           `json:"requests_total"`
	ErrorRate     float64       `json:"error_rate"`
	LatencyMS     LatencyMS     `json:"latency_ms"`
	ByStatus      []StatusCount `json:"by_status"`
	StartedAt     time.Time     `json:"started_at"`
	UptimeSeconds int64         `json:"uptime_seconds"`
}

func (m *Registry) Snapshot() Snapshot {
	out := Snapshot{
		ByStatus:      []StatusCount{},
		StartedAt:     m.startedAt,
		UptimeSeconds: int64(time.Since(m.startedAt).Seconds()),
	}
	families, err := m.reg.Gather()
	if err != nil {
		return out
	}

	var errCount int
	var latencyCount uint64
	var latencySum float64
	buckets := map[float64]uint64{}

	for _, family := range families {
		switch family.GetName() {
		case "gateway_requests_total":
			for _, metric := range family.GetMetric() {
				labels := map[string]string{}
				for _, pair := range metric.GetLabel() {
					labels[pair.GetName()] = pair.GetValue()
				}
				count := int(metric.GetCounter().GetValue())
				out.RequestsTotal += count
				if !strings.HasPrefix(labels["status"], "2") {
					errCount += count
				}
				out.ByStatus = append(out.ByStatus, StatusCount{
					Provider: labels["provider"],
					Status:   labels["status"],
					Count:    count,
				})
			}
		case "gateway_request_duration_seconds":
			for _, metric := range family.GetMetric() {
				h := metric.GetHistogram()
				latencyCount += h.GetSampleCount()
				latencySum += h.GetSampleSum()
				for _, bucket := range h.GetBucket() {
					buckets[bucket.GetUpperBound()] += bucket.GetCumulativeCount()
				}
			}
		}
	}

	if out.RequestsTotal > 0 {
		out.ErrorRate = float64(errCount) / float64(out.RequestsTotal)
	}
	if latencyCount > 0 {
		out.LatencyMS.Avg = latencySum / float64(latencyCount) * 1000
		out.LatencyMS.P50 = percentile(buckets, latencyCount, 0.50) * 1000
		out.LatencyMS.P95 = percentile(buckets, latencyCount, 0.95) * 1000
	}
	return out
}

func percentile(buckets map[float64]uint64, count uint64, p float64) float64 {
	if count == 0 {
		return 0
	}
	bounds := make([]float64, 0, len(buckets))
	for bound := range buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)

	target := p * float64(count)
	var prevBound float64
	var prevCum uint64
	for _, bound := range bounds {
		cum := buckets[bound]
		if float64(cum) >= target {
			if math.IsInf(bound, 1) || cum == prevCum {
				return prevBound
			}
			frac := (target - float64(prevCum)) / float64(cum-prevCum)
			return prevBound + frac*(bound-prevBound)
		}
		prevBound, prevCum = bound, cum
	}
	return bounds[len(bounds)-1]
}
