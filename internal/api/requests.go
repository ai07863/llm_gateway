package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/usage"
)

func registerRequests(v1 *gin.RouterGroup, repo usage.Repo) {
	v1.GET("/admin/requests", func(c *gin.Context) {
		q := usage.Query{
			Model:    c.Query("model"),
			Provider: c.Query("provider"),
			Q:        strings.TrimSpace(c.Query("q")),
		}
		switch c.Query("cache") {
		case "", "all":
		case "hit":
			v := true
			q.Cache = &v
		case "miss":
			v := false
			q.Cache = &v
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "cache must be hit, miss or all"}})
			return
		}

		limit, err := intParam(c.Query("limit"), 50, 1, 200)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "limit must be a number between 1 and 200"}})
			return
		}
		offset, err := intParam(c.Query("offset"), 0, 0, 1<<30)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "offset must be a non-negative number"}})
			return
		}
		q.Limit = limit
		q.Offset = offset

		total, records, err := repo.Query(c.Request.Context(), q)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"total": total, "records": records})
	})
}

func intParam(raw string, def, min, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}
