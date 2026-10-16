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
	cfg    config.ProviderConfig
	client *http.Client
}

func NewOpenAICompat(cfg config.ProviderConfig) Provider {
	return &openAICompat{
		cfg: cfg,
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
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

func (p *openAICompat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := json.Marshal(req)
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
		return ChatResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return ChatResponse{}, fmt.Errorf("provider %s status %d: %s", p.cfg.Name, res.StatusCode, string(b))
	}
	var parsed oaChatResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return ChatResponse{}, err
	}
	out := ChatResponse{
		Model:            parsed.Model,
		TotalTokens:      parsed.Usage.TotalTokens,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
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
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("embed status %d", res.StatusCode)
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
	req.Stream = true
	body, err := json.Marshal(struct {
		ChatRequest
		StreamOptions map[string]bool `json:"stream_options,omitempty"`
	}{
		ChatRequest:   req,
		StreamOptions: map[string]bool{"include_usage": true},
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	res, err := p.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, fmt.Errorf("provider %s status %d: %s", p.cfg.Name, res.StatusCode, string(b))
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
			if len(parsed.Choices) > 0 && parsed.Choices[0].Delta.Content != "" {
				if !send(StreamChunk{Content: parsed.Choices[0].Delta.Content}) {
					return
				}
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
