package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/cache"
	"llm-gateway/internal/config"
	"llm-gateway/internal/pricing"
	"llm-gateway/internal/provider"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/usage"
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
	repo, err := usage.NewSQLite(s.DBPath)
	if err != nil {
		panic(err)
	}
	registerModels(v1, models)
	registerAdmin(v1, repo)

	exact := cache.NewExact(10 * time.Minute)

	recordUsage := func(resp provider.ChatResponse, p provider.Provider) {
		_ = repo.Record(context.Background(), usage.Record{
			ID:               newRequestID(),
			Model:            resp.Model,
			Provider:         p.Name(),
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
			Cost:             pricing.Cost(resp.Model, resp.PromptTokens, resp.CompletionTokens),
		})
	}

	registerChat(v1,
		func(c *gin.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
			key := exact.Key(req)
			if cached, ok := exact.Get(key); ok {
				return provider.ChatResponse{Model: req.Model, Content: cached}, nil
			}
			if len(providers) == 0 {
				return provider.ChatResponse{}, errors.New("no provider configured")
			}
			resp, used, err := proxy.ChatWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				return provider.ChatResponse{}, err
			}
			exact.Set(key, resp.Content)
			recordUsage(resp, used)
			return resp, nil
		},
		func(c *gin.Context, req provider.ChatRequest) error {
			if len(providers) == 0 {
				return errors.New("no provider configured")
			}
			ch, _, err := proxy.StreamWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				return err
			}
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Stream(func(w io.Writer) bool {
				chunk, ok := <-ch
				if !ok || chunk.Done {
					fmt.Fprint(w, "data: [DONE]\n\n")
					return false
				}
				fmt.Fprintf(w, "data: %s\n\n", mustJSON(gin.H{"choices": []gin.H{{"delta": gin.H{"content": chunk.Content}}}}))
				return true
			})
			return nil
		},
	)

	return r
}
