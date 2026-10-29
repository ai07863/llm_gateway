package pricing

import (
	_ "embed"
	"encoding/json"
	"sync"
	"time"

	_ "time/tzdata"
)

//go:embed default.json
var defaultJSON []byte

// NowFunc is replaceable in tests.
var NowFunc = time.Now

var location = mustLoadLocation("Asia/Shanghai")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

type Rate struct {
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

type OffPeak struct {
	In    float64 `json:"in"`
	Out   float64 `json:"out"`
	Start string  `json:"start"`
	End   string  `json:"end"`
}

type ModelPrice struct {
	In      float64  `json:"in"`
	Out     float64  `json:"out"`
	OffPeak *OffPeak `json:"off_peak,omitempty"`
}

type Doc struct {
	Models map[string]ModelPrice `json:"models"`
}

type Store struct {
	mu          sync.RWMutex
	models      map[string]ModelPrice
	lastRefresh time.Time
	source      string
	lastErr     string
}

var store = func() *Store {
	return &Store{models: loadDefault(), source: "embedded"}
}()

func loadDefault() map[string]ModelPrice {
	var doc Doc
	if err := json.Unmarshal(defaultJSON, &doc); err != nil {
		panic("pricing: invalid embedded default.json: " + err.Error())
	}
	return doc.Models
}

func Cost(model string, promptTokens, completionTokens int) float64 {
	return costAt(model, promptTokens, completionTokens, NowFunc())
}

func costAt(model string, promptTokens, completionTokens int, now time.Time) float64 {
	store.mu.RLock()
	mp, ok := store.models[model]
	store.mu.RUnlock()
	if !ok {
		return 0
	}
	r := effectiveRate(mp, now)
	return float64(promptTokens)/1e6*r.In + float64(completionTokens)/1e6*r.Out
}

func effectiveRate(mp ModelPrice, now time.Time) Rate {
	if mp.OffPeak != nil && inWindow(now.In(location), mp.OffPeak.Start, mp.OffPeak.End) {
		return Rate{In: mp.OffPeak.In, Out: mp.OffPeak.Out}
	}
	return Rate{In: mp.In, Out: mp.Out}
}

func inWindow(now time.Time, start, end string) bool {
	s, ok := parseClock(start)
	if !ok {
		s = 30 // 00:30 默认空闲起点
	}
	e, ok := parseClock(end)
	if !ok {
		e = 8*60 + 30 // 08:30 默认空闲终点
	}
	if s == e {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if s < e {
		return m >= s && m < e
	}
	return m >= s || m < e
}

func parseClock(hhmm string) (int, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func Score(model string) (float64, bool) {
	store.mu.RLock()
	mp, ok := store.models[model]
	store.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return mp.In + mp.Out, true
}
