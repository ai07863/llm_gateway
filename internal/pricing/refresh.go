package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

const maxPricingBytes = 1 << 20 // 1MB

var httpClient = &http.Client{Timeout: 10 * time.Second}

type Snapshot struct {
	Source      string                `json:"source"`
	LastRefresh time.Time             `json:"last_refresh"`
	LastError   string                `json:"last_error,omitempty"`
	Models      map[string]ModelPrice `json:"models"`
}

func Refresh(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		err := fmt.Errorf("pricing: unsupported url scheme %q", u.Scheme)
		store.setErr(err.Error())
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		err := fmt.Errorf("pricing: refresh status %d", res.StatusCode)
		store.setErr(err.Error())
		return err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPricingBytes+1))
	if err != nil {
		store.setErr(err.Error())
		return err
	}
	if len(body) > maxPricingBytes {
		err := fmt.Errorf("pricing: payload exceeds %d bytes", maxPricingBytes)
		store.setErr(err.Error())
		return err
	}
	var doc Doc
	if err := json.Unmarshal(body, &doc); err != nil {
		store.setErr(err.Error())
		return err
	}
	if err := validateDoc(doc); err != nil {
		store.setErr(err.Error())
		return err
	}
	merged := loadDefault()
	for name, mp := range doc.Models {
		merged[name] = mp
	}
	store.replace(merged, rawURL)
	return nil
}

func validateDoc(doc Doc) error {
	for name, mp := range doc.Models {
		if mp.In < 0 || mp.Out < 0 {
			return fmt.Errorf("pricing: model %s has negative rate", name)
		}
		if mp.OffPeak != nil {
			if mp.OffPeak.In < 0 || mp.OffPeak.Out < 0 {
				return fmt.Errorf("pricing: model %s off_peak negative rate", name)
			}
			if _, ok := parseClock(mp.OffPeak.Start); !ok {
				return fmt.Errorf("pricing: model %s bad off_peak.start %q", name, mp.OffPeak.Start)
			}
			if _, ok := parseClock(mp.OffPeak.End); !ok {
				return fmt.Errorf("pricing: model %s bad off_peak.end %q", name, mp.OffPeak.End)
			}
		}
	}
	return nil
}

func (s *Store) setErr(msg string) {
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
}

func (s *Store) replace(models map[string]ModelPrice, source string) {
	s.mu.Lock()
	s.models = models
	s.source = source
	s.lastRefresh = NowFunc()
	s.lastErr = ""
	s.mu.Unlock()
}

func Take() Snapshot {
	store.mu.RLock()
	defer store.mu.RUnlock()
	models := make(map[string]ModelPrice, len(store.models))
	for k, v := range store.models {
		models[k] = v
	}
	return Snapshot{Source: store.source, LastRefresh: store.lastRefresh, LastError: store.lastErr, Models: models}
}

func StartAutoRefresh(rawURL string, interval time.Duration) {
	if rawURL == "" {
		return
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	go func() {
		ctx := context.Background()
		if err := Refresh(ctx, rawURL); err != nil {
			log.Printf("pricing: initial refresh failed: %v", err)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := Refresh(ctx, rawURL); err != nil {
				log.Printf("pricing: refresh failed: %v", err)
			}
		}
	}()
}
