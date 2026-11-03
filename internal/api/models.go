package api

import "github.com/gin-gonic/gin"

func registerModels(v1 *gin.RouterGroup, models []string) {
	v1.GET("/models", func(c *gin.Context) {
		data := make([]gin.H, 0, len(models))
		for _, m := range models {
			data = append(data, gin.H{"id": m, "object": "model"})
		}
		c.JSON(200, gin.H{"object": "list", "data": data})
	})
}
