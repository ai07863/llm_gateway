package similarity

import "testing"

func TestCosine(t *testing.T) {
	if got := Cosine([]float64{1, 0}, []float64{1, 0}); got < 0.999 {
		t.Fatalf("identical want ~1, got %v", got)
	}
	if got := Cosine([]float64{1, 0}, []float64{0, 1}); got > 0.001 {
		t.Fatalf("orthogonal want ~0, got %v", got)
	}
}