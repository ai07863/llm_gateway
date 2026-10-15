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
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(dashboardHTML))
	})
}