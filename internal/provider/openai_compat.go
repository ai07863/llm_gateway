package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"llm-gateway/internal/config"
)

type openAICompat struct {
	cfg    config.ProviderConfig
	client *http.Client
}

func NewOpenAICompat(cfg config.ProviderConfig) Provider {
	return &openAICompat{cfg: cfg, client: &http.Client{}}
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