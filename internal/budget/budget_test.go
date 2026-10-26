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

func TestCheaperModelPicksCheapestConfigured(t *testing.T) {
	cands := []Model{{Name: "deepseek-chat", Price: 1.37}, {Name: "gpt-4o-mini", Price: 5.5}}
	if got := CheaperModel("gpt-4o-mini", 5.5, cands); got != "deepseek-chat" {
		t.Fatalf("want deepseek-chat, got %q", got)
	}
	if got := CheaperModel("deepseek-chat", 1.37, cands); got != "deepseek-chat" {
		t.Fatalf("already cheapest stays same, got %q", got)
	}
}

func TestCheaperModelUnknownCurrentDoesNotDegrade(t *testing.T) {
	cands := []Model{{Name: "deepseek-chat", Price: 1.37}}
	if got := CheaperModel("mystery", 0, cands); got != "mystery" {
		t.Fatalf("unknown current price must not degrade, got %q", got)
	}
}

func TestCheaperModelNoCheaperCandidateStays(t *testing.T) {
	cands := []Model{{Name: "gpt-4o-mini", Price: 5.5}}
	if got := CheaperModel("gpt-4o-mini", 5.5, cands); got != "gpt-4o-mini" {
		t.Fatalf("no cheaper configured model must not change, got %q", got)
	}
}
