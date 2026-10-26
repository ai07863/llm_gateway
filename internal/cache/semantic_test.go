package cache

import (
	"context"
	"testing"
)

type fakeEmbedder struct{}

func (fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if t == "" {
			out[i] = []float64{0, 1}
		} else {
			out[i] = []float64{1, 0}
		}
	}
	return out, nil
}

func TestSemanticLookupHit(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	vecs, _ := fakeEmbedder{}.Embed(ctx, []string{"hello world"})
	if err := s.Store("scope", vecs[0], CachedResult{Content: "answer", PromptTokens: 3, CompletionTokens: 4}); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, _, ok := s.Lookup(ctx, "scope", "hi there")
	if !ok || got.Content != "answer" || got.PromptTokens != 3 || got.CompletionTokens != 4 {
		t.Fatalf("want hit with tokens, got %+v %v", got, ok)
	}
}

func TestSemanticScopeIsolation(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	vecs, _ := fakeEmbedder{}.Embed(ctx, []string{"hello world"})
	if err := s.Store("scope-a", vecs[0], CachedResult{Content: "answer-a"}); err != nil {
		t.Fatalf("store: %v", err)
	}
	if got, _, ok := s.Lookup(ctx, "scope-b", "hi there"); ok {
		t.Fatalf("different scope must not hit, got %+v", got)
	}
	if got, _, ok := s.Lookup(ctx, "scope-a", "hi there"); !ok || got.Content != "answer-a" {
		t.Fatalf("same scope want hit 'answer-a', got %+v %v", got, ok)
	}
}

func TestSemanticStoreEmptyVectorIsNoop(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	if err := s.Store("scope", nil, CachedResult{Content: "answer"}); err != nil {
		t.Fatalf("store with nil vector should be no-op, got %v", err)
	}
	if got, _, ok := s.Lookup(context.Background(), "scope", "x"); ok {
		t.Fatalf("nothing should have been stored, got %+v", got)
	}
}
