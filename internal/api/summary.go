package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/usage"
)

type budgetStatus struct {
	Limit     float64 `json:"limit"`
	SoftRatio float64 `json:"soft_ratio"`
	Cost      float64 `json:"cost"`
	Saved     float64 `json:"saved"`
	UsedRatio float64 `json:"used_ratio"`
	Degraded  bool    `json:"degraded"`
	Rejected  bool    `json:"rejected"`
}

type summaryResponse struct {
	usage.Summary
	Budget budgetStatus `json:"budget"`
}

func registerSummary(v1 *gin.RouterGroup, repo usage.Repo, s config.Settings) {
	v1.GET("/admin/summary", func(c *gin.Context) {
		month := c.DefaultQuery("month", usage.CurrentMonth())
		if _, err := time.Parse("2006-01", month); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid month, want YYYY-MM"}})
			return
		}

		summary, err := repo.Summary(c.Request.Context(), month)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		cost, _ := repo.MonthlyCost(c.Request.Context(), month)
		saved, _ := repo.MonthlySaved(c.Request.Context(), month)

		budget := budgetStatus{
			Limit:     s.BudgetMonthlyLimit,
			SoftRatio: s.BudgetSoftRatio,
			Cost:      cost,
			Saved:     saved,
		}
		if budget.Limit > 0 {
			budget.UsedRatio = cost / budget.Limit
			budget.Degraded = cost >= budget.Limit*budget.SoftRatio && cost < budget.Limit
			budget.Rejected = cost >= budget.Limit
		}
		c.JSON(http.StatusOK, summaryResponse{Summary: summary, Budget: budget})
	})
}