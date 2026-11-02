package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"llm-gateway/internal/config"
	"llm-gateway/internal/proxy"
	"llm-gateway/internal/usage"
)

func TestResponsesEndpointFailsOverAndPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldMax, oldBackoff := proxy.MaxAttempts, proxy.RetryBackoff
	proxy.MaxAttempts, proxy.RetryBackoff = 1, time.Millisecond
	defer func() { proxy.MaxAttempts, proxy.RetryBackoff = oldMax, oldBackoff }()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer primary.Close()

	var backupModel string
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		backupModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"id":"resp_1","model":"backup-model","output":[],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	}))
	defer backup.Close()

	db := "file:responses_router?mode=memory&cache=shared"
	s := config.Settings{
		GatewayToken: "tok", DBPath: db, BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{
			{Name: "primary", BaseURL: primary.URL, APIKey: "k", Model: "primary-model"},
			{Name: "backup", BaseURL: backup.URL, APIKey: "k", Model: "backup-model"},
		},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"primary-model","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%q", w.Code, w.Body.String())
	}
	if backupModel != "backup-model" {
		t.Fatalf("backup provider must receive its own model, got %q", backupModel)
	}
	if !strings.Contains(w.Body.String(), `"id":"resp_1"`) {
		t.Fatalf("body not passed through: %q", w.Body.String())
	}

	repo, err := usage.NewSQLite(db)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer repo.Close()
	records, err := repo.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list usage: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("want 1 usage record, got %d: %+v", len(records), records)
	}
	if records[0].Model != "backup-model" {
		t.Fatalf("usage must be attributed to served model, got %q", records[0].Model)
	}
	if records[0].Provider != "backup" {
		t.Fatalf("usage must be attributed to serving provider, got %q", records[0].Provider)
	}
}
