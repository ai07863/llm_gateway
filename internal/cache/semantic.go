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

// Lookup embeds text once and returns the best matching answer for the given
// scope. The query vector is returned too (when the embed succeeds) so the
// caller can reuse it for Store and avoid a second embedding call.
func (s *Semantic) Lookup(ctx context.Context, scope, text string) (string, []float64, bool) {
	vecs, err := s.embedder.Embed(ctx, []string{text})
	if err != nil || len(vecs) == 0 {
		return "", nil, false
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
		return best, q, true
	}
	return "", q, false
}

// Store records answer under scope using a precomputed query vector (from
// Lookup). An empty vector is a no-op, so a failed embed never breaks a request.
func (s *Semantic) Store(scope string, qvec []float64, answer string) error {
	if len(qvec) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, semanticEntry{scope: scope, vec: qvec, answer: answer})
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
	return nil
}
