package metrics

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	reg      *prometheus.Registry
	Requests *prometheus.CounterVec
	Latency  *prometheus.HistogramVec
	Tokens   *prometheus.CounterVec
}

func New() *Registry {
	reg := prometheus.NewRegistry()
	m := &Registry{
		reg: reg,
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total", Help: "total requests",
		}, []string{"provider", "status"}),
		Latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gateway_request_duration_seconds", Help: "request latency",
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
