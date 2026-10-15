package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/budget"
	"llm-gateway/internal/cache"
	"llm-gateway/internal/config"
	"llm-gateway/internal/metrics"
	"llm-gateway/internal/pricing"
	"llm-gateway/internal/provider"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/usage"
)

func NewRouter(s config.Settings) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), RequestIDMiddleware())
	registerDashboard(r)
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	m := metrics.New()
	r.GET("/metrics", m.Handler())

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
	var semantic *cache.Semantic
	if len(providers) > 0 && s.Providers[0].EmbedModel != "" {
		if emb, ok := providers[0].(provider.Embedder); ok {
			semantic = cache.NewSemantic(emb, 0.95, 2000)
		}
	}

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
			lastUser := lastUserMessage(req.Messages)
			scope := semanticScope(req)
			if semantic != nil {
				if answer, ok := semantic.Lookup(c.Request.Context(), scope, lastUser); ok {
					return provider.ChatResponse{Model: req.Model, Content: answer}, nil
				}
			}
			cost, _ := repo.MonthlyCost(c.Request.Context(), usage.CurrentMonth())
			switch budget.Evaluate(cost, s.BudgetMonthlyLimit, s.BudgetSoftRatio) {
			case budget.Reject:
				return provider.ChatResponse{}, &StatusError{Status: http.StatusTooManyRequests, Msg: "monthly budget exceeded"}
			case budget.Degrade:
				req.Model = budget.CheaperModel(req.Model)
			}
			if len(providers) == 0 {
				return provider.ChatResponse{}, errors.New("no provider configured")
			}
			start := time.Now()
			resp, used, err := proxy.ChatWithFailover(c.Request.Context(), providers, req)
			if err != nil {
				return provider.ChatResponse{}, err
			}
			exact.Set(key, resp.Content)
			if semantic != nil {
				_ = semantic.Store(c.Request.Context(), scope, lastUser, resp.Content)
			}
			recordUsage(resp, used)
			m.Latency.WithLabelValues(used.Name()).Observe(time.Since(start).Seconds())
			m.Requests.WithLabelValues(used.Name(), "200").Inc()
			m.Tokens.WithLabelValues(used.Name(), "prompt").Add(float64(resp.PromptTokens))
			m.Tokens.WithLabelValues(used.Name(), "completion").Add(float64(resp.CompletionTokens))
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

func lastUserMessage(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func systemPrompt(messages []provider.Message) string {
	var b strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			b.WriteString(m.Content)
			b.WriteByte('\x00')
		}
	}
	return b.String()
}

func semanticScope(req provider.ChatRequest) string {
	return req.Model + "\x00" + systemPrompt(req.Messages)
}
