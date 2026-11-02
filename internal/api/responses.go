package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type responsesProbe struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func registerResponses(
	v1 *gin.RouterGroup,
	responses func(c *gin.Context, req provider.ResponsesRequest) (provider.ResponsesResult, error),
	responsesStream func(c *gin.Context, req provider.ResponsesRequest) error,
) {
	v1.POST("/responses", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		var probe responsesProbe
		if err := json.Unmarshal(raw, &probe); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		if probe.Model == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "model is required"}})
			return
		}
		req := provider.ResponsesRequest{Model: probe.Model, Stream: probe.Stream, Raw: raw}
		if probe.Stream {
			if err := responsesStream(c, req); err != nil {
				writeResponsesError(c, err)
			}
			return
		}
		res, err := responses(c, req)
		if err != nil {
			writeResponsesError(c, err)
			return
		}
		c.Data(http.StatusOK, "application/json", res.Raw)
	})
}

// writeResponsesError mirrors writeError but passes a 4xx upstream body through
// untouched so clients see the provider's native error shape.
func writeResponsesError(c *gin.Context, err error) {
	var pe *provider.Error
	if errors.As(err, &pe) && pe.StatusCode >= 400 && pe.StatusCode < 500 && pe.Body != "" {
		c.Data(pe.StatusCode, "application/json", []byte(pe.Body))
		return
	}
	var se *StatusError
	if errors.As(err, &se) {
		c.JSON(se.Status, gin.H{"error": gin.H{"message": se.Msg}})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
}

// writeResponsesSSE forwards frames verbatim. Responses streams are terminated
// by response.completed, so no [DONE] sentinel is appended.
func writeResponsesSSE(c *gin.Context, ch <-chan provider.ResponsesChunk) (int, int, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	var prompt, completion int
	var streamErr error
	c.Stream(func(w io.Writer) bool {
		chunk, ok := <-ch
		if !ok {
			return false
		}
		if chunk.Err != nil {
			streamErr = chunk.Err
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(gin.H{"error": gin.H{"message": chunk.Err.Error()}}))
			return false
		}
		if chunk.Raw != "" {
			fmt.Fprint(w, chunk.Raw)
		}
		if chunk.Done {
			prompt, completion = chunk.PromptTokens, chunk.CompletionTokens
			return false
		}
		return true
	})
	return prompt, completion, streamErr
}
