package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
)

func NewRouter(s config.Settings) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), RequestIDMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	v1 := r.Group("/v1", AuthMiddleware(s.GatewayToken))
	_ = v1 // 后续任务注册 chat/embeddings/models
	return r
}