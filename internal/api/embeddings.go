package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/provider"
)

type embeddingBody struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

func registerEmbeddings(v1 *gin.RouterGroup, providers []provider.Provider) {
	v1.POST("/embeddings", func(c *gin.Context) {
		var body embeddingBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		inputs, err := parseEmbeddingInput(body.Input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		if len(providers) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "no provider configured"}})
			return
		}
		embedder, ok := providers[0].(provider.Embedder)
		if !ok {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "provider does not support embeddings"}})
			return
		}
		vecs, err := embedder.Embed(c.Request.Context(), inputs)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		data := make([]gin.H, len(vecs))
		for i, v := range vecs {
			data[i] = gin.H{"object": "embedding", "index": i, "embedding": v}
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": data, "model": body.Model})
	})
}

func parseEmbeddingInput(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("input is required")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many, nil
	}
	return nil, errors.New("input must be a string or an array of strings")
}
