package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type ProviderConfig struct {
	Name       string
	BaseURL    string
	APIKey     string
	Model      string
	Models     []string
	EmbedModel string
}

// ModelList returns every routable model, always including the primary Model.
// Callers that build ProviderConfig directly (tests) may omit Models.
func (p ProviderConfig) ModelList() []string {
	if len(p.Models) > 0 {
		return p.Models
	}
	if p.Model == "" {
		return nil
	}
	return []string{p.Model}
}

// HasModel reports whether model is routable through this provider.
func (p ProviderConfig) HasModel(model string) bool {
	for _, m := range p.ModelList() {
		if m == model {
			return true
		}
	}
	return false
}

type Settings struct {
	GatewayToken           string
	ListenAddr             string
	DBPath                 string
	BudgetMonthlyLimit     float64
	BudgetSoftRatio        float64
	PricingURL             string
	PricingRefreshInterval time.Duration
	// RetentionDays > 0 prunes usage rows older than this; 0 keeps everything.
	RetentionDays int
	Providers     []ProviderConfig
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
		RetentionDays:          getint("RETENTION_DAYS", 0),
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		model := getenv("DEEPSEEK_MODEL", "deepseek-chat")
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "deepseek", BaseURL: getenv("DEEPSEEK_BASE_URL", "https://api.deepseek.com/v1"),
			APIKey: key, Model: model, Models: modelList(model, "DEEPSEEK_MODELS"),
			EmbedModel: os.Getenv("DEEPSEEK_EMBED_MODEL"),
		})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		model := getenv("OPENAI_MODEL", "gpt-4o-mini")
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "openai", BaseURL: getenv("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			APIKey: key, Model: model, Models: modelList(model, "OPENAI_MODELS"),
			EmbedModel: os.Getenv("OPENAI_EMBED_MODEL"),
		})
	}
	// Alibaba Cloud DashScope (通义千问) exposes an OpenAI-compatible endpoint.
	if key := os.Getenv("DASHSCOPE_API_KEY"); key != "" {
		model := getenv("DASHSCOPE_MODEL", "qwen-plus")
		s.Providers = append(s.Providers, ProviderConfig{
			Name: "dashscope", BaseURL: getenv("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
			APIKey: key, Model: model, Models: modelList(model, "DASHSCOPE_MODELS"),
			EmbedModel: os.Getenv("DASHSCOPE_EMBED_MODEL"),
		})
	}
	return s, nil
}

// modelList builds the routable model list: the primary model first, then any
// comma-separated extras from envKey, de-duplicated and trimmed.
func modelList(primary, envKey string) []string {
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	add(primary)
	for _, m := range strings.Split(os.Getenv(envKey), ",") {
		add(m)
	}
	return out
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

func getint(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
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
