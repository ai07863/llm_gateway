package cache

import (
	"testing"
	"time"

	"llm-gateway/internal/provider"
)

func TestExactCacheHitAndMiss(t *testing.T) {
	c := NewExact(time.Minute)
	req := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	key := c.Key(req)

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss")
	}
	c.Set(key, "answer")
	if v, ok := c.Get(key); !ok || v != "answer" {
		t.Fatalf("expected hit 'answer', got %q %v", v, ok)
	}
}

func TestExactCacheExpiry(t *testing.T) {
	now := time.Now()
	NowFunc = func() time.Time { return now }
	defer func() { NowFunc = time.Now }()

	c := NewExact(time.Second)
	key := "k"
	c.Set(key, "v")
	now = now.Add(2 * time.Second)
	if _, ok := c.Get(key); ok {
		t.Fatal("expected expired")
	}
}
