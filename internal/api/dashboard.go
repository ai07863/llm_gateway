package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var dashboardHTML string

func registerDashboard(r *gin.Engine) {
	r.GET("/", func(c *gin.Context) {
		// Never cache the dashboard: browsers otherwise keep serving an old build
		// (e.g. a previous polling version) after the gateway is updated.
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(dashboardHTML))
	})
}
