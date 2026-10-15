package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/provider"
)

func NewRouter(s config.Settings) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), RequestIDMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	v1 := r.Group("/v1", AuthMiddleware(s.GatewayToken))

	providers := make([]provider.Provider, 0, len(s.Providers))
	models := make([]string, 0, len(s.Providers))
	for _, pc := range s.Providers {
		providers = append(providers, provider.NewOpenAICompat(pc))
		models = append(models, pc.Model)
	}
	registerModels(v1, models)
	registerChat(v1, func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
		if len(providers) == 0 {
			return provider.ChatResponse{}, errors.New("no provider configured")
		}
		resp, err := providers[0].Chat(c.Request.Context(), req)
		if err != nil {
			return provider.ChatResponse{}, err
		}
		return resp, nil
	})

	return r
}