package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type chatBody struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature *float64           `json:"temperature"`
}

func registerChat(v1 *gin.RouterGroup, chat func(c *gin.Context, req provider.ChatRequest) provider.ChatResponse) {
	v1.POST("/chat/completions", func(c *gin.Context) {
		var body chatBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		resp := chat(c, provider.ChatRequest{Model: body.Model, Messages: body.Messages, Temperature: body.Temperature})
		c.JSON(http.StatusOK, gin.H{
			"model": resp.Model,
			"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": resp.Content}}},
			"usage": gin.H{
				"prompt_tokens": resp.PromptTokens, "completion_tokens": resp.CompletionTokens, "total_tokens": resp.TotalTokens,
			},
		})
	})
}