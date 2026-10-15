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
	if err := s.Store(ctx, "scope", "hello world", "answer"); err != nil {
		t.Fatalf("store: %v", err)
	}
	v, ok := s.Lookup(ctx, "scope", "hi there")
	if !ok || v != "answer" {
		t.Fatalf("want hit 'answer', got %q %v", v, ok)
	}
}

func TestSemanticScopeIsolation(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	if err := s.Store(ctx, "scope-a", "hello world", "answer-a"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if v, ok := s.Lookup(ctx, "scope-b", "hi there"); ok {
		t.Fatalf("different scope must not hit, got %q", v)
	}
	if v, ok := s.Lookup(ctx, "scope-a", "hi there"); !ok || v != "answer-a" {
		t.Fatalf("same scope want hit 'answer-a', got %q %v", v, ok)
	}
}