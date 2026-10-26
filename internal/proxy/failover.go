package proxy

import (
	"context"
	"errors"
	"time"

	"llm-gateway/internal/provider"
)

var (
	MaxAttempts  = 3
	RetryBackoff = 200 * time.Millisecond
)

type Target struct {
	Provider provider.Provider
	Model    string
}

func ChatWithFailover(ctx context.Context, targets []Target, req provider.ChatRequest) (provider.ChatResponse, provider.Provider, error) {
	if len(targets) == 0 {
		return provider.ChatResponse{}, nil, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return provider.ChatResponse{}, nil, ctx.Err()
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		resp, err := doWithRetry(ctx, func() (provider.ChatResponse, error) {
			return tgt.Provider.Chat(ctx, attemptReq)
		})
		if err == nil {
			return resp, tgt.Provider, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return provider.ChatResponse{}, nil, err
		}
	}
	return provider.ChatResponse{}, nil, lastErr
}

func StreamWithFailover(ctx context.Context, targets []Target, req provider.ChatRequest) (<-chan provider.StreamChunk, provider.Provider, error) {
	if len(targets) == 0 {
		return nil, nil, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		ch, err := doWithRetry(ctx, func() (<-chan provider.StreamChunk, error) {
			return tgt.Provider.ChatStream(ctx, attemptReq)
		})
		if err == nil {
			return ch, tgt.Provider, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return nil, nil, err
		}
	}
	return nil, nil, lastErr
}

func doWithRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	attempts := MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) || attempt == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(RetryBackoff << attempt):
		}
	}
	return zero, lastErr
}
