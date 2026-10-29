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
	c.Set(key, CachedResult{Content: "answer", PromptTokens: 10, CompletionTokens: 20})
	got, ok := c.Get(key)
	if !ok || got.Content != "answer" || got.PromptTokens != 10 || got.CompletionTokens != 20 {
		t.Fatalf("expected hit with tokens, got %+v %v", got, ok)
	}
}

func TestExactCacheKeyDistinguishesTemperature(t *testing.T) {
	c := NewExact(time.Minute)
	zero := 0.0
	one := 1.0
	base := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}

	reqZero := base
	reqZero.Temperature = &zero
	reqOne := base
	reqOne.Temperature = &one

	keyZero := c.Key(reqZero)
	keyOne := c.Key(reqOne)
	if keyZero == keyOne {
		t.Fatal("keys for different temperatures must differ")
	}

	c.Set(keyZero, CachedResult{Content: "zero-answer"})
	if _, ok := c.Get(keyOne); ok {
		t.Fatal("value cached under temperature=0 must not be returned for temperature=1")
	}
	if got, ok := c.Get(keyZero); !ok || got.Content != "zero-answer" {
		t.Fatalf("expected hit 'zero-answer', got %+v %v", got, ok)
	}
}

func TestExactCacheExpiry(t *testing.T) {
	now := time.Now()
	NowFunc = func() time.Time { return now }
	defer func() { NowFunc = time.Now }()

	c := NewExact(time.Second)
	key := "k"
	c.Set(key, CachedResult{Content: "v"})
	now = now.Add(2 * time.Second)
	if _, ok := c.Get(key); ok {
		t.Fatal("expected expired")
	}
}

func TestExactCacheEvictsOldestWhenFull(t *testing.T) {
	c := NewExactSized(time.Hour, 2)
	c.Set("k1", CachedResult{Content: "v1"})
	c.Set("k2", CachedResult{Content: "v2"})
	c.Set("k3", CachedResult{Content: "v3"})

	if _, ok := c.Get("k1"); ok {
		t.Fatal("oldest entry k1 should have been evicted")
	}
	if _, ok := c.Get("k2"); !ok {
		t.Fatal("k2 should still be present")
	}
	if _, ok := c.Get("k3"); !ok {
		t.Fatal("k3 should be present")
	}
}
