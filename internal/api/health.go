package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/metrics"
)

func registerHealth(v1 *gin.RouterGroup, m *metrics.Registry) {
	v1.GET("/admin/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, m.Snapshot())
	})
}
