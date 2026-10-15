package proxy

import (
	"context"

	"llm-gateway/internal/provider"
)

func ChatWithFailover(ctx context.Context, providers []provider.Provider, req provider.ChatRequest) (provider.ChatResponse, provider.Provider, error) {
	var lastErr error
	for _, p := range providers {
		resp, err := p.Chat(ctx, req)
		if err == nil {
			return resp, p, nil
		}
		lastErr = err
	}
	return provider.ChatResponse{}, nil, lastErr
}

func StreamWithFailover(ctx context.Context, providers []provider.Provider, req provider.ChatRequest) (<-chan provider.StreamChunk, provider.Provider, error) {
	var lastErr error
	for _, p := range providers {
		ch, err := p.ChatStream(ctx, req)
		if err == nil {
			return ch, p, nil
		}
		lastErr = err
	}
	return nil, nil, lastErr
}