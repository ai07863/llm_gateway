package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"llm-gateway/internal/provider"
)

var NowFunc = time.Now

type entry struct {
	value   string
	expires time.Time
}

type Exact struct {
	mu  sync.RWMutex
	ttl time.Duration
	m   map[string]entry
}

func NewExact(ttl time.Duration) *Exact {
	return &Exact{ttl: ttl, m: make(map[string]entry)}
}

func (c *Exact) Key(req provider.ChatRequest) string {
	b, _ := json.Marshal(struct {
		Model       string
		Messages    []provider.Message
		Temperature *float64
	}{req.Model, req.Messages, req.Temperature})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (c *Exact) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[key]
	if !ok || NowFunc().After(e.expires) {
		return "", false
	}
	return e.value, true
}

func (c *Exact) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = entry{value: value, expires: NowFunc().Add(c.ttl)}
}
