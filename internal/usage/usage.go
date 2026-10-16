package usage

import (
	"context"
	"time"
)

type Record struct {
	ID               string    `json:"id"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Cost             float64   `json:"cost"`
	CacheHit         bool      `json:"cache_hit"`
	CreatedAt        time.Time `json:"created_at"`
}

type Repo interface {
	Record(ctx context.Context, r Record) error
	MonthlyCost(ctx context.Context, yyyymm string) (float64, error)
	List(ctx context.Context, limit int) ([]Record, error)
	Close() error
}

// NowFunc is replaceable in tests.
var NowFunc = time.Now

func CurrentMonth() string { return NowFunc().Format("2006-01") }
