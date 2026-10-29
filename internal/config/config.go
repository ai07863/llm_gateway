package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type ProviderConfig struct {
	Name       string
	BaseURL    string
	APIKey     string
	Model      string
	EmbedModel string
}

type Settings struct {
	GatewayToken           string
	ListenAddr             string
	DBPath                 string
	BudgetMonthlyLimit     float64
	BudgetSoftRatio        float64
	PricingURL             string
	PricingRefreshInterval time.Duration
	Providers              []ProviderConfig
}

func Load() (Settings, error) {
	_ = godotenv.Load()
	s := Settings{
		GatewayToken:           os.Getenv("GATEWAY_TOKEN"),
		ListenAddr:             getenv("LISTEN_ADDR", ":8080"),
		DBPath:                 getenv("DB_PATH", "gateway.db"),
		BudgetMonthlyLimit:     getfloat("BUDGET_MONTHLY_LIMIT", 20),
		BudgetSoftRatio:        getfloat("BUDGET_SOFT_RATIO", 0.8),
		PricingURL:             os.Getenv("PRICING_URL"),
		PricingRefreshInterval: getduration("PRICING_REFRESH_INTERVAL", 6*time.Hour),
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "deepseek", BaseURL: getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			APIKey: key, Model: getenv("DEEPSEEK_MODEL", "deepseek-chat"),
			EmbedModel: os.Getenv("DEEPSEEK_EMBED_MODEL"),
		})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "openai", BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey: key, Model: getenv("OPENAI_MODEL", "gpt-4o-mini"),
			EmbedModel: os.Getenv("OPENAI_EMBED_MODEL"),
		})
	}
	// Alibaba Cloud DashScope (通义千问) exposes an OpenAI-compatible endpoint.
	if key := os.Getenv("DASHSCOPE_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "dashscope", BaseURL: getenv("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			APIKey: key, Model: getenv("DASHSCOPE_MODEL", "qwen-plus"),
			EmbedModel: os.Getenv("DASHSCOPE_EMBED_MODEL"),
		})
	}
	return s, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getfloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// getduration keeps the default interval when the value is unset, malformed,
// or non-positive, so a bad env var can't disable the pricing refresh loop.
func getduration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
