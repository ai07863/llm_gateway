package provider

import (
	"context"
	"encoding/json"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	// Raw is the client's original request body. When set it is forwarded
	// verbatim (with only "model" rewritten) so fields this gateway does not
	// model — tools, tool_choice, response_format, max_tokens, ... — still reach
	// the upstream. Without it we fall back to marshaling the struct.
	Raw json.RawMessage `json:"-"`
}

type StreamChunk struct {
	Content string
	// Raw is the original SSE data payload (without the "data: " prefix) for
	// transparent pass-through of tool_call deltas and other fields.
	Raw              string
	Done             bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Err              error
}

type ChatResponse struct {
	Model            string `json:"model"`
	Content          string `json:"content"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	// Raw is the upstream response body, returned to the client verbatim so
	// tool_calls and other fields survive.
	Raw []byte `json:"-"`
}

type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error)
}

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
}
