package pricing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func snapshotStore(t *testing.T) {
	t.Helper()
	store.mu.RLock()
	savedModels := store.models
	savedRefresh := store.lastRefresh
	savedSource := store.source
	savedErr := store.lastErr
	store.mu.RUnlock()
	t.Cleanup(func() {
		store.mu.Lock()
		store.models = savedModels
		store.lastRefresh = savedRefresh
		store.source = savedSource
		store.lastErr = savedErr
		store.mu.Unlock()
	})
}

func TestRefreshMergesRemoteOverDefault(t *testing.T) {
	snapshotStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{
			"deepseek-chat":{"in":9,"out":9},
			"new-model":{"in":3,"out":4}
		}}`))
	}))
	defer srv.Close()

	if err := Refresh(context.Background(), srv.URL); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if c := costAt("deepseek-chat", 1_000_000, 0, NowFunc()); c != 9 {
		t.Fatalf("remote should override deepseek-chat in=9, got %v", c)
	}
	if c := costAt("new-model", 1_000_000, 0, NowFunc()); c != 3 {
		t.Fatalf("remote should add new-model in=3, got %v", c)
	}
	if _, ok := Score("qwen-plus"); !ok {
		t.Fatal("models not in remote must keep embedded default")
	}
	snap := Take()
	if snap.Source != srv.URL || snap.LastError != "" || snap.LastRefresh.IsZero() {
		t.Fatalf("snapshot after refresh unexpected: %+v", snap)
	}
}

func TestRefreshRejectsNonHTTPScheme(t *testing.T) {
	snapshotStore(t)
	if err := Refresh(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
}

func TestRefreshKeepsOldTableOnError(t *testing.T) {
	snapshotStore(t)
	before := costAt("deepseek-chat", 1_000_000, 0, NowFunc())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("non-2xx must error")
	}

	if after := costAt("deepseek-chat", 1_000_000, 0, NowFunc()); after != before {
		t.Fatalf("failed refresh must not change table: before %v after %v", before, after)
	}
	if Take().LastError == "" {
		t.Fatal("failed refresh should record last_error")
	}
}

func TestRefreshRejectsBadJSON(t *testing.T) {
	snapshotStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("bad json must error")
	}
}

func TestRefreshRejectsBadWindow(t *testing.T) {
	snapshotStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"m":{"in":1,"out":1,"off_peak":{"in":1,"out":1,"start":"99:99","end":"08:30"}}}}`))
	}))
	defer srv.Close()
	if err := Refresh(context.Background(), srv.URL); err == nil {
		t.Fatal("bad off_peak window must error")
	}
}
