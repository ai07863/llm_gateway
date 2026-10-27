package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/pricing"
)

func registerPricing(v1 *gin.RouterGroup) {
	v1.GET("/admin/pricing", func(c *gin.Context) {
		c.JSON(http.StatusOK, pricing.Take())
	})
}
