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

// CachedResult carries the cached completion together with the token counts
// that produced it, so a cache hit can still be attributed to cost savings.
type CachedResult struct {
	Content          string
	PromptTokens     int
	CompletionTokens int
}

type entry struct {
	res     CachedResult
	expires time.Time
}

// defaultMaxEntries bounds the exact cache so a long-running gateway does not
// grow without limit (TTL alone only prevents hits, never reclaims memory).
const defaultMaxEntries = 5000

type Exact struct {
	mu    sync.RWMutex
	ttl   time.Duration
	max   int
	m     map[string]entry
	order []string
}

func NewExact(ttl time.Duration) *Exact {
	return NewExactSized(ttl, defaultMaxEntries)
}

func NewExactSized(ttl time.Duration, max int) *Exact {
	return &Exact{ttl: ttl, max: max, m: make(map[string]entry)}
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

func (c *Exact) Get(key string) (CachedResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[key]
	if !ok || NowFunc().After(e.expires) {
		return CachedResult{}, false
	}
	return e.res, true
}

func (c *Exact) Set(key string, res CachedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[key]; !exists {
		c.order = append(c.order, key)
	}
	c.m[key] = entry{res: res, expires: NowFunc().Add(c.ttl)}
	c.evictLocked()
}

// evictLocked drops expired entries, then oldest-inserted entries, until the
// cache is within its size bound. Caller must hold the write lock.
func (c *Exact) evictLocked() {
	if c.max <= 0 {
		return
	}
	now := NowFunc()
	for k, e := range c.m {
		if now.After(e.expires) {
			delete(c.m, k)
		}
	}
	for len(c.m) > c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
}
