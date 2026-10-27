package pricing

import (
	"testing"
	"time"
)

func withNow(t *testing.T, at time.Time) {
	t.Helper()
	old := NowFunc
	NowFunc = func() time.Time { return at }
	t.Cleanup(func() { NowFunc = old })
}

func TestCostKnownModel(t *testing.T) {
	withNow(t, time.Date(2026, 1, 1, 12, 0, 0, 0, location))
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

func TestDeepSeekV41FlashPeakAndOffPeak(t *testing.T) {
	withNow(t, time.Date(2026, 1, 1, 12, 0, 0, 0, location)) // 12:00 高峰
	got := Cost("deepseek-v4.1-flash", 1_000_000, 1_000_000)
	if diff := got - 10.0; diff > 1e-9 || diff < -1e-9 { // 2 + 8
		t.Fatalf("peak want 10, got %v", got)
	}
	withNow(t, time.Date(2026, 1, 1, 3, 0, 0, 0, location)) // 03:00 空闲
	got = Cost("deepseek-v4.1-flash", 1_000_000, 1_000_000)
	if diff := got - 5.0; diff > 1e-9 || diff < -1e-9 { // 1 + 4
		t.Fatalf("off-peak want 5, got %v", got)
	}
}

func TestOffPeakBoundaries(t *testing.T) {
	cases := []struct {
		hh, mm int
		want   float64 // 每百万输入价：高峰 2，空闲 1
	}{
		{0, 29, 2}, {0, 30, 1}, {8, 29, 1}, {8, 30, 2}, {23, 59, 2},
	}
	for _, tc := range cases {
		withNow(t, time.Date(2026, 1, 1, tc.hh, tc.mm, 0, 0, location))
		got := Cost("deepseek-v4.1-flash", 1_000_000, 0)
		if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("%02d:%02d want %v got %v", tc.hh, tc.mm, tc.want, got)
		}
	}
}

func TestInWindowCrossMidnight(t *testing.T) {
	mp := ModelPrice{In: 2, Out: 8, OffPeak: &OffPeak{In: 1, Out: 4, Start: "22:00", End: "06:00"}}
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, location) }
	if effectiveRate(mp, at(23)).In != 1 || effectiveRate(mp, at(2)).In != 1 {
		t.Fatal("23:00 and 02:00 should be off-peak for 22:00-06:00")
	}
	if effectiveRate(mp, at(12)).In != 2 || effectiveRate(mp, at(6)).In != 2 {
		t.Fatal("12:00 and 06:00 should be peak for 22:00-06:00")
	}
}

func TestScoreUsesPeakRate(t *testing.T) {
	s, ok := Score("deepseek-v4.1-flash")
	if !ok || s != 10 {
		t.Fatalf("want score 10 (peak 2+8), got %v %v", s, ok)
	}
	if _, ok := Score("nope"); ok {
		t.Fatal("unknown model must report not-found")
	}
}

func TestDefaultTableHasModels(t *testing.T) {
	for _, m := range []string{"deepseek-chat", "gpt-4o-mini", "qwen-turbo", "qwen-plus", "qwen-max", "qwen3.8-flash", "deepseek-v4.1-flash"} {
		if _, ok := Score(m); !ok {
			t.Fatalf("embedded default table missing %q", m)
		}
	}
}
