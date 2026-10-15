package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/usage"
)

func registerAdmin(v1 *gin.RouterGroup, repo usage.Repo) {
	v1.GET("/admin/usage", func(c *gin.Context) {
		records, err := repo.List(c.Request.Context(), 100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		month, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
		c.JSON(http.StatusOK, gin.H{"month_cost": month, "records": records})
	})
}