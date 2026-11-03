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
	SavedCost        float64   `json:"saved_cost"`
	CacheHit         bool      `json:"cache_hit"`
	CreatedAt        time.Time `json:"created_at"`
}

type Totals struct {
	Requests         int     `json:"requests"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	Saved            float64 `json:"saved"`
	CacheHits        int     `json:"cache_hits"`
	CacheHitRatio    float64 `json:"cache_hit_ratio"`
}

type ModelAgg struct {
	Model string `json:"model"`
	Totals
}

type ProviderAgg struct {
	Provider string `json:"provider"`
	Totals
}

type DayBucket struct {
	Day string `json:"day"`
	Totals
}

type HourBucket struct {
	Hour string `json:"hour"`
	Totals
}

type Summary struct {
	Month      string        `json:"month"`
	Totals     Totals        `json:"totals"`
	ByModel    []ModelAgg    `json:"by_model"`
	ByProvider []ProviderAgg `json:"by_provider"`
	ByDay      []DayBucket   `json:"by_day"`
	ByHour     []HourBucket  `json:"by_hour"`
}

type Query struct {
	Model    string
	Provider string
	Cache    *bool
	Q        string
	Limit    int
	Offset   int
}

type Repo interface {
	Record(ctx context.Context, r Record) error
	MonthlyCost(ctx context.Context, yyyymm string) (float64, error)
	MonthlySaved(ctx context.Context, yyyymm string) (float64, error)
	List(ctx context.Context, limit int) ([]Record, error)
	Summary(ctx context.Context, month string) (Summary, error)
	Query(ctx context.Context, q Query) (int, []Record, error)
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
	Close() error
}

func ratio(hits, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// NowFunc is replaceable in tests.
var NowFunc = time.Now

func CurrentMonth() string { return NowFunc().Format("2006-01") }
