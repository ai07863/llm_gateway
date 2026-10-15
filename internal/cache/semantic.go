package cache

import (
	"context"
	"sync"

	"llm-gateway/internal/provider"
	"llm-gateway/internal/similarity"
)

type semanticEntry struct {
	scope  string
	vec    []float64
	answer string
}

type Semantic struct {
	mu        sync.RWMutex
	embedder  provider.Embedder
	threshold float64
	max       int
	entries   []semanticEntry
}

func NewSemantic(e provider.Embedder, threshold float64, max int) *Semantic {
	return &Semantic{embedder: e, threshold: threshold, max: max}
}

func (s *Semantic) Lookup(ctx context.Context, scope, text string) (string, bool) {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return "", false
	}
	q := vecs[0]
	s.mu.RLock()
	defer s.mu.RUnlock()
	best, bestScore := "", 0.0
	for _, e := range s.entries {
		if e.scope != scope {
			continue
		}
		if sc := similarity.Cosine(q, e.vec); sc > bestScore {
			bestScore, best = sc, e.answer
		}
	}
	if bestScore >= s.threshold {
		return best, true
	}
	return "", false
}

func (s *Semantic) Store(ctx context.Context, scope, text, answer string) error {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, semanticEntry{scope: scope, vec: vecs[0], answer: answer})
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
	return nil
}