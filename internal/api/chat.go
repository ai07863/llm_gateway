package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type chatBody struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature *float64           `json:"temperature"`
	Stream      bool               `json:"stream"`
}

func registerChat(
	v1 *gin.RouterGroup,
	chat func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error),
	chatStream func(c *gin.Context, req provider.ChatRequest) error,
) {
	v1.POST("/chat/completions", func(c *gin.Context) {
		var body chatBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		req := provider.ChatRequest{Model: body.Model, Messages: body.Messages, Temperature: body.Temperature}
		if body.Stream {
			if err := chatStream(c, req); err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			}
			return
		}
		resp, err := chat(c, req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"model":   resp.Model,
			"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": resp.Content}}},
			"usage": gin.H{
				"prompt_tokens": resp.PromptTokens, "completion_tokens": resp.CompletionTokens, "total_tokens": resp.TotalTokens,
			},
		})
	})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
