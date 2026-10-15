package config

import (
	"os"
	"strconv"
)

type ProviderConfig struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
}

type Settings struct {
	GatewayToken       string
	ListenAddr         string
	DBPath             string
	BudgetMonthlyLimit float64
	BudgetSoftRatio    float64
	Providers          []ProviderConfig
}

func Load() (Settings, error) {
	s := Settings{
		GatewayToken:       os.Getenv("GATEWAY_TOKEN"),
		ListenAddr:         getenv("LISTEN_ADDR", ":8080"),
		DBPath:             getenv("DB_PATH", "gateway.db"),
		BudgetMonthlyLimit: getfloat("BUDGET_MONTHLY_LIMIT", 20),
		BudgetSoftRatio:    getfloat("BUDGET_SOFT_RATIO", 0.8),
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "deepseek", BaseURL: getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			APIKey: key, Model: getenv("DEEPSEEK_MODEL", "deepseek-chat"),
		})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "openai", BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey: key, Model: getenv("OPENAI_MODEL", "gpt-4o-mini"),
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