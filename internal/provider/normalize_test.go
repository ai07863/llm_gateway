package provider

import (
	"encoding/json"
	"testing"
)

func messagesOf(t *testing.T, body []byte) []map[string]json.RawMessage {
	t.Helper()
	var payload struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, body)
	}
	return payload.Messages
}

func roleOf(t *testing.T, msg map[string]json.RawMessage) string {
	t.Helper()
	var role string
	if err := json.Unmarshal(msg["role"], &role); err != nil {
		t.Fatalf("decode role: %v", err)
	}
	return role
}

func TestBuildBodyRewritesDeveloperRole(t *testing.T) {
	raw := json.RawMessage(`{
		"model":"m",
		"messages":[
			{"role":"developer","content":[{"type":"text","text":"be brief"}],"name":"codex"},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"shell"}}]
	}`)

	body, err := buildBody(ChatRequest{Model: "m", Raw: raw}, false)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	msgs := messagesOf(t, body)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d: %s", len(msgs), body)
	}
	if got := roleOf(t, msgs[0]); got != "system" {
		t.Fatalf("developer must become system, got %q", got)
	}
	if got := string(msgs[0]["content"]); got != `[{"type":"text","text":"be brief"}]` {
		t.Fatalf("content must be preserved, got %s", got)
	}
	if got := string(msgs[0]["name"]); got != `"codex"` {
		t.Fatalf("extra fields must be preserved, got %s", got)
	}
	if got := roleOf(t, msgs[1]); got != "user" {
		t.Fatalf("user role must be untouched, got %q", got)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload["tools"]) == "" {
		t.Fatalf("tools must be forwarded, body=%s", body)
	}
}

func TestBuildBodyKeepsSystemAndToolRoles(t *testing.T) {
	raw := json.RawMessage(`{
		"model":"m",
		"messages":[
			{"role":"system","content":"a"},
			{"role":"assistant","content":"b"},
			{"role":"tool","content":"c","tool_call_id":"x"}
		]
	}`)

	body, err := buildBody(ChatRequest{Model: "m", Raw: raw}, false)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	msgs := messagesOf(t, body)
	want := []string{"system", "assistant", "tool"}
	for i, role := range want {
		if got := roleOf(t, msgs[i]); got != role {
			t.Fatalf("message %d: want %q, got %q", i, role, got)
		}
	}
}

func TestBuildBodyRewritesDeveloperRoleInStructAndStream(t *testing.T) {
	body, err := buildBody(ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "developer", Content: "be brief"}, {Role: "user", Content: "hi"}},
	}, true)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	msgs := messagesOf(t, body)
	if got := roleOf(t, msgs[0]); got != "system" {
		t.Fatalf("developer must become system, got %q", got)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload["stream"]) != "true" {
		t.Fatalf("stream flag missing: %s", body)
	}
	if string(payload["stream_options"]) == "" {
		t.Fatalf("stream_options missing: %s", body)
	}
}
