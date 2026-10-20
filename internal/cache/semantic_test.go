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
	if err := s.Store("scope", vecs[0], "answer"); err != nil {
		t.Fatalf("store: %v", err)
	}
	v, _, ok := s.Lookup(ctx, "scope", "hi there")
	if !ok || v != "answer" {
		t.Fatalf("want hit 'answer', got %q %v", v, ok)
	}
}

func TestSemanticScopeIsolation(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	ctx := context.Background()
	vecs, _ := fakeEmbedder{}.Embed(ctx, []string{"hello world"})
	if err := s.Store("scope-a", vecs[0], "answer-a"); err != nil {
		t.Fatalf("store: %v", err)
	}
	if v, _, ok := s.Lookup(ctx, "scope-b", "hi there"); ok {
		t.Fatalf("different scope must not hit, got %q", v)
	}
	if v, _, ok := s.Lookup(ctx, "scope-a", "hi there"); !ok || v != "answer-a" {
		t.Fatalf("same scope want hit 'answer-a', got %q %v", v, ok)
	}
}

func TestSemanticStoreEmptyVectorIsNoop(t *testing.T) {
	s := NewSemantic(fakeEmbedder{}, 0.9, 100)
	if err := s.Store("scope", nil, "answer"); err != nil {
		t.Fatalf("store with nil vector should be no-op, got %v", err)
	}
	if v, _, ok := s.Lookup(context.Background(), "scope", "x"); ok {
		t.Fatalf("nothing should have been stored, got %q", v)
	}
}
