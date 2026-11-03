package api

import (
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
)

func TestChatFailsOverToBackupWithItsOwnModel(t *testing.T) {
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
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		backupModel, _ = req["model"].(string)
		_, _ = io.WriteString(w, `{"model":"backup-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer backup.Close()

	s := config.Settings{
		GatewayToken: "tok", DBPath: "file:routing?mode=memory&cache=shared", BudgetMonthlyLimit: 100,
		Providers: []config.ProviderConfig{
			{Name: "primary", BaseURL: primary.URL, APIKey: "k", Model: "primary-model"},
			{Name: "backup", BaseURL: backup.URL, APIKey: "k", Model: "backup-model"},
		},
	}
	r := NewRouter(s)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"primary-model","messages":[{"role":"user","content":"hi"}]}`))
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
}
