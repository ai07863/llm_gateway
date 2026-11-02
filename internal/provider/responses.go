package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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

type responsesEvent struct {
	Type     string `json:"type"`
	Response struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

func (p *openAICompat) ResponsesStream(ctx context.Context, req ResponsesRequest) (<-chan ResponsesChunk, error) {
	body, err := prepareResponsesBody(req.Raw, req.Model, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.streamClient.Do(httpReq)
	if err != nil {
		return nil, NewNetworkError(p.cfg.Name, err)
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}

	ch := make(chan ResponsesChunk)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		send := func(c ResponsesChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}

		var frame strings.Builder
		var prompt, completion, total int
		completed := false
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		flush := func() bool {
			if frame.Len() == 0 {
				return true
			}
			raw := frame.String() + "\n"
			frame.Reset()
			return send(ResponsesChunk{Raw: raw})
		}

		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if line == "" {
				if !flush() {
					return
				}
				continue
			}
			frame.WriteString(line)
			frame.WriteByte('\n')
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				var ev responsesEvent
				if err := json.Unmarshal([]byte(payload), &ev); err == nil && ev.Type == "response.completed" {
					completed = true
					prompt = ev.Response.Usage.InputTokens
					completion = ev.Response.Usage.OutputTokens
					total = ev.Response.Usage.TotalTokens
				}
			}
		}
		flush()
		if err := scanner.Err(); err != nil {
			send(ResponsesChunk{Done: true, Err: err, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		if !completed {
			send(ResponsesChunk{Done: true, Err: errors.New("upstream stream ended before response.completed"), PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		send(ResponsesChunk{Done: true, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
	}()
	return ch, nil
}
