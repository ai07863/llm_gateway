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
