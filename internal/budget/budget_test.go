package budget

import "testing"

func TestEvaluate(t *testing.T) {
	// limit=20, softRatio=0.8 → soft 阈值 16
	cases := []struct {
		cost float64
		want Decision
	}{
		{5, Allow},
		{15.99, Allow},
		{16, Degrade},
		{19.99, Degrade},
		{20, Reject},
		{25, Reject},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.cost, 20, 0.8); got != tc.want {
			t.Fatalf("cost=%v want %v got %v", tc.cost, tc.want, got)
		}
	}
}

func TestCheaperModel(t *testing.T) {
	if CheaperModel("gpt-4o-mini") != "deepseek-chat" {
		t.Fatal("gpt-4o-mini should degrade to deepseek-chat")
	}
	if CheaperModel("deepseek-chat") != "deepseek-chat" {
		t.Fatal("already cheapest stays same")
	}
}
