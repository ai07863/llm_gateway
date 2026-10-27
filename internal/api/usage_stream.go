package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/usage"
)

// usageHub fans out newly recorded usage to connected dashboards. Each
// subscriber gets its own buffered channel so a slow client can never block a
// request; dropped events are reconciled by the dashboard's fallback refresh.
type usageHub struct {
	mu   sync.Mutex
	subs map[chan usage.Record]struct{}
}

func newUsageHub() *usageHub {
	return &usageHub{subs: make(map[chan usage.Record]struct{})}
}

func (h *usageHub) subscribe() chan usage.Record {
	ch := make(chan usage.Record, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *usageHub) unsubscribe(ch chan usage.Record) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *usageHub) publish(rec usage.Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- rec:
		default:
		}
	}
}

func registerUsageStream(v1 *gin.RouterGroup, hub *usageHub) {
	v1.GET("/admin/usage/stream", func(c *gin.Context) {
		ch := hub.subscribe()
		defer hub.unsubscribe(ch)
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		// Flush a comment immediately so the browser's fetch resolves and knows
		// the stream is live before the first usage event arrives.
		fmt.Fprint(c.Writer, ": connected\n\n")
		c.Writer.Flush()
		c.Stream(func(w io.Writer) bool {
			select {
			case rec, ok := <-ch:
				if !ok {
					return false
				}
				b, _ := json.Marshal(rec)
				fmt.Fprintf(w, "data: %s\n\n", b)
				return true
			case <-c.Request.Context().Done():
				return false
			}
		})
	})
}
