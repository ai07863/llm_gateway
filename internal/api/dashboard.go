package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var dashboardHTML string

//go:embed web/chart.umd.min.js
var chartJS []byte

func registerDashboard(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		// Never cache the dashboard: browsers otherwise keep serving an old build
		// (e.g. a previous polling version) after the gateway is updated.
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(dashboardHTML))
	})
	r.GET("/assets/chart.js", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", chartJS)
	})
}
