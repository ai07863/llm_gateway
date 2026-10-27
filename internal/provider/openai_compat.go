package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"llm-gateway/internal/config"
)

type openAICompat struct {
	cfg          config.ProviderConfig
	client       *http.Client
	streamClient *http.Client
}

func NewOpenAICompat(cfg config.ProviderConfig) Provider {
	// Clone http.DefaultTransport so we keep its pool tuning and honors
	// HTTP(S)_PROXY via ProxyFromEnvironment, then bound header latency only.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 30 * time.Second
	return &openAICompat{
		cfg:          cfg,
		client:       &http.Client{Timeout: 60 * time.Second, Transport: tr},
		streamClient: &http.Client{Transport: tr.Clone()},
	}
}

func (p *openAICompat) Name() string { return p.cfg.Name }

type oaChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func buildBody(req ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) == 0 {
		r := req
		r.Stream = stream || req.Stream
		if stream {
			return json.Marshal(struct {
				ChatRequest
				StreamOptions map[string]bool `json:"stream_options,omitempty"`
			}{r, map[string]bool{"include_usage": true}})
		}
		return json.Marshal(r)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(req.Raw, &m); err != nil {
		return nil, err
	}
	model, err := json.Marshal(req.Model)
	if err != nil {
		return nil, err
	}
	m["model"] = model
	if stream {
		m["stream"] = json.RawMessage("true")
		m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return json.Marshal(m)
}

func (p *openAICompat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := buildBody(req, false)
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	res, err := p.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ChatResponse{}, NewStatusError(p.cfg.Name, res.StatusCode, string(b))
	}
	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return ChatResponse{}, err
	}
	var parsed oaChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, err
	}
	out := ChatResponse{
		Model:            parsed.Model,
		TotalTokens:      parsed.Usage.TotalTokens,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		Raw:              respBody,
	}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
	}
	return out, nil
}

type oaEmbedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func (p *openAICompat) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if p.cfg.EmbedModel == "" {
		return nil, fmt.Errorf("no embed model configured for provider %s", p.cfg.Name)
	}
	body, err := json.Marshal(map[string]any{"model": p.cfg.EmbedModel, "input": texts})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	res, err := p.client.Do(httpReq)
	if err != nil {
		return nil, NewNetworkError(p.cfg.Name, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, NewStatusError(p.cfg.Name, res.StatusCode, "")
	}
	var parsed oaEmbedResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([][]float64, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

func (p *openAICompat) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	body, err := buildBody(req, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
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

	ch := make(chan StreamChunk)
	go func() {
		defer close(ch)
		defer res.Body.Close()
		send := func(c StreamChunk) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		var prompt, completion, total int
		sawDone := false
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				sawDone = true
				break
			}
			var parsed struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
				continue
			}
			if parsed.Usage.PromptTokens != 0 || parsed.Usage.CompletionTokens != 0 || parsed.Usage.TotalTokens != 0 {
				prompt, completion, total = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, parsed.Usage.TotalTokens
			}
			content := ""
			if len(parsed.Choices) > 0 {
				content = parsed.Choices[0].Delta.Content
			}
			// Forward the payload verbatim so tool_call deltas and fields this
			// gateway does not model reach the client unchanged.
			if !send(StreamChunk{Raw: payload, Content: content}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			send(StreamChunk{Done: true, Err: err, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		if !sawDone {
			send(StreamChunk{Done: true, Err: errors.New("upstream stream ended without [DONE]"), PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
			return
		}
		send(StreamChunk{Done: true, PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total})
	}()
	return ch, nil
}
