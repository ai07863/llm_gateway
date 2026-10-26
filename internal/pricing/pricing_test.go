package pricing

import "testing"

func TestCostKnownModel(t *testing.T) {
	// deepseek-chat: 输入 0.27 元/百万 token，输出 1.10 元/百万 token（示例价）
	got := Cost("deepseek-chat", 1_000_000, 1_000_000)
	want := 1.37
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestCostUnknownModelFallsBack(t *testing.T) {
	if Cost("unknown-model", 1000, 1000) != 0 {
		t.Fatal("unknown model should cost 0")
	}
}

func TestScore(t *testing.T) {
	s, ok := Score("deepseek-chat")
	if !ok || s < 1.36 || s > 1.38 {
		t.Fatalf("want deepseek-chat score ~1.37, got %v %v", s, ok)
	}
	if _, ok := Score("nope"); ok {
		t.Fatal("unknown model must report not-found")
	}
}
