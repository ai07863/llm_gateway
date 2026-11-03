package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string { return e.Msg }

func writeError(c *gin.Context, err error) {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		c.JSON(statusErr.Status, gin.H{"error": gin.H{"message": statusErr.Msg}})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
}

// chatProbe is a lenient view of the request: messages are kept as raw JSON so
// tool-call / multimodal message shapes still pass through untouched.
type chatProbe struct {
	Model       string          `json:"model"`
	Messages    json.RawMessage `json:"messages"`
	Temperature *float64        `json:"temperature"`
	Stream      bool            `json:"stream"`
	Tools       json.RawMessage `json:"tools"`
}

func registerChat(
	v1 *gin.RouterGroup,
	chat func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error),
	chatStream func(c *gin.Context, req provider.ChatRequest) error,
) {
	v1.POST("/chat/completions", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		var probe chatProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		var msgs []provider.Message
		if len(probe.Messages) > 0 {
			// Best effort: only used for cache keys and semantic lookup. A shape
			// we cannot parse simply means no cache benefit, not a failure.
			_ = json.Unmarshal(probe.Messages, &msgs)
		}
		req := provider.ChatRequest{
			Model:       probe.Model,
			Messages:    msgs,
			Temperature: probe.Temperature,
			Tools:       probe.Tools,
			Raw:         raw,
		}
		if probe.Stream {
			if err := chatStream(c, req); err != nil {
				writeError(c, err)
			}
			return
		}
		resp, err := chat(c, req)
		if err != nil {
			writeError(c, err)
			return
		}
		if len(resp.Raw) > 0 {
			c.Data(http.StatusOK, "application/json", resp.Raw)
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
