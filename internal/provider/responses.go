package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type ResponsesRequest struct {
	Model  string
	Stream bool
	Raw    []byte
}

type ResponsesResult struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Raw              []byte
}

type ResponsesChunk struct {
	Raw              string
	Done             bool
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Err              error
}

type ResponsesProvider interface {
	Responses(ctx context.Context, req ResponsesRequest) (ResponsesResult, error)
	ResponsesStream(ctx context.Context, req ResponsesRequest) (<-chan ResponsesChunk, error)
}

type responsesPayload struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// prepareResponsesBody rewrites the top-level model and, for streams, forces
// stream=true while leaving every other client field untouched.
func prepareResponsesBody(raw []byte, model string, stream bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	mb, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = mb
	if stream {
		m["stream"] = json.RawMessage("true")
	}
	return json.Marshal(m)
}

func (p *openAICompat) Responses(ctx context.Context, req ResponsesRequest) (ResponsesResult, error) {
	body, err := prepareResponsesBody(req.Raw, req.Model, false)
	if err != nil {
		return ResponsesResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return ResponsesResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return ResponsesResult{}, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ResponsesResult{}, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}
	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return ResponsesResult{}, err
	}
	var parsed responsesPayload
	_ = json.Unmarshal(respBody, &parsed)
	model := parsed.Model
	if model == "" {
		model = req.Model
	}
	return ResponsesResult{
		Model:            model,
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
		Raw:              respBody,
	}, nil
}
