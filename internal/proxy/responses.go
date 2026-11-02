package proxy

import (
	"context"
	"errors"

	"llm-gateway/internal/provider"
)

func ResponsesWithFailover(ctx context.Context, targets []Target, req provider.ResponsesRequest) (provider.ResponsesResult, Target, error) {
	if len(targets) == 0 {
		return provider.ResponsesResult{}, Target{}, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return provider.ResponsesResult{}, Target{}, ctx.Err()
		}
		rp, ok := tgt.Provider.(provider.ResponsesProvider)
		if !ok {
			continue
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		res, err := doWithRetry(ctx, func() (provider.ResponsesResult, error) {
			return rp.Responses(ctx, attemptReq)
		})
		if err == nil {
			res.Model = tgt.Model
			return res, tgt, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return provider.ResponsesResult{}, Target{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no provider supports the responses API")
	}
	return provider.ResponsesResult{}, Target{}, lastErr
}

func ResponsesStreamWithFailover(ctx context.Context, targets []Target, req provider.ResponsesRequest) (<-chan provider.ResponsesChunk, Target, error) {
	if len(targets) == 0 {
		return nil, Target{}, errors.New("no providers available")
	}
	var lastErr error
	for _, tgt := range targets {
		if ctx.Err() != nil {
			return nil, Target{}, ctx.Err()
		}
		rp, ok := tgt.Provider.(provider.ResponsesProvider)
		if !ok {
			continue
		}
		attemptReq := req
		attemptReq.Model = tgt.Model
		ch, err := doWithRetry(ctx, func() (<-chan provider.ResponsesChunk, error) {
			return rp.ResponsesStream(ctx, attemptReq)
		})
		if err == nil {
			return ch, tgt, nil
		}
		lastErr = err
		if !provider.IsRetryable(err) {
			return nil, Target{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no provider supports the responses API")
	}
	return nil, Target{}, lastErr
}
