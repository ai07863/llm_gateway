package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDotEnv(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func TestLoadAutoLoadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "GATEWAY_TOKEN=from-dotenv\nBUDGET_MONTHLY_LIMIT=42\nDEEPSEEK_API_KEY=sk-test\n")
	clearEnv(t, "GATEWAY_TOKEN", "BUDGET_MONTHLY_LIMIT", "DEEPSEEK_API_KEY", "OPENAI_API_KEY")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.GatewayToken != "from-dotenv" {
		t.Fatalf("want token from .env, got %q", s.GatewayToken)
	}
	if s.BudgetMonthlyLimit != 42 {
		t.Fatalf("want budget 42, got %v", s.BudgetMonthlyLimit)
	}
	if len(s.Providers) != 1 || s.Providers[0].Name != "deepseek" {
		t.Fatalf("want deepseek provider from .env, got %+v", s.Providers)
	}
}

func TestLoadAddsDashscopeProvider(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "DASHSCOPE_API_KEY=sk-test\n")
	clearEnv(t, "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "DASHSCOPE_API_KEY",
		"DASHSCOPE_BASE_URL", "DASHSCOPE_MODEL", "DASHSCOPE_EMBED_MODEL")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var got *ProviderConfig
	for i := range s.Providers {
		if s.Providers[i].Name == "dashscope" {
			got = &s.Providers[i]
		}
	}
	if got == nil {
		t.Fatalf("want dashscope provider from .env, got %+v", s.Providers)
	}
	if got.BaseURL != "https://dashscope.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("want dashscope OpenAI-compatible base url, got %q", got.BaseURL)
	}
	if got.Model != "qwen-plus" {
		t.Fatalf("want default model qwen-plus, got %q", got.Model)
	}
	if got.APIKey != "sk-test" {
		t.Fatalf("want key from .env, got %q", got.APIKey)
	}
}

func TestLoadEnvTakesPrecedenceOverDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "GATEWAY_TOKEN=from-dotenv\n")
	clearEnv(t, "GATEWAY_TOKEN")
	t.Setenv("GATEWAY_TOKEN", "from-env")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.GatewayToken != "from-env" {
		t.Fatalf("want env to win over .env, got %q", s.GatewayToken)
	}
}

func TestLoadPricingSettings(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "PRICING_URL=https://example.com/pricing.json\nPRICING_REFRESH_INTERVAL=30m\n")
	clearEnv(t, "PRICING_URL", "PRICING_REFRESH_INTERVAL")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.PricingURL != "https://example.com/pricing.json" {
		t.Fatalf("want pricing url from .env, got %q", s.PricingURL)
	}
	if s.PricingRefreshInterval != 30*time.Minute {
		t.Fatalf("want 30m interval, got %v", s.PricingRefreshInterval)
	}
}

func TestLoadPricingDefaultsTo6h(t *testing.T) {
	dir := t.TempDir()
	writeDotEnv(t, dir, "GATEWAY_TOKEN=x\n")
	clearEnv(t, "PRICING_URL", "PRICING_REFRESH_INTERVAL", "GATEWAY_TOKEN")
	chdir(t, dir)

	s, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.PricingURL != "" {
		t.Fatalf("want empty pricing url, got %q", s.PricingURL)
	}
	if s.PricingRefreshInterval != 6*time.Hour {
		t.Fatalf("want 6h default, got %v", s.PricingRefreshInterval)
	}
}
