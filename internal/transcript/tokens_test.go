package transcript

import (
	"testing"
)

func TestEstimateContextNoAnchor(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}
	e := EstimateContext(msgs)
	if e.LastUsageIndex != -1 {
		t.Fatalf("expected no anchor, got %d", e.LastUsageIndex)
	}
	if e.Tokens != e.TrailingTokens {
		t.Fatalf("Tokens should equal TrailingTokens with no anchor")
	}
	if e.Tokens <= 0 {
		t.Fatalf("expected positive tokens, got %d", e.Tokens)
	}
}

func TestEstimateContextWithAnchor(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world", Usage: &Usage{TotalTokens: 100}},
		{Role: "user", Content: "next"},
	}
	e := EstimateContext(msgs)
	if e.LastUsageIndex != 1 {
		t.Fatalf("expected anchor at 1, got %d", e.LastUsageIndex)
	}
	if e.UsageTokens != 100 {
		t.Fatalf("expected usage 100, got %d", e.UsageTokens)
	}
	if e.TrailingTokens <= 0 {
		t.Fatalf("expected positive trailing tokens, got %d", e.TrailingTokens)
	}
	if e.Tokens != e.UsageTokens+e.TrailingTokens {
		t.Fatalf("Tokens mismatch: %d != %d + %d", e.Tokens, e.UsageTokens, e.TrailingTokens)
	}
}

func TestMeterPercentRemaining(t *testing.T) {
	m := Meter{Used: 50, Limit: 100, Stale: false}
	pct, ok := m.PercentRemaining()
	if !ok {
		t.Fatal("expected ok")
	}
	if pct != 50 {
		t.Fatalf("expected 50%%, got %d", pct)
	}

	m2 := Meter{Used: 150, Limit: 100}
	pct2, ok2 := m2.PercentRemaining()
	if !ok2 {
		t.Fatal("expected ok even when over limit")
	}
	if pct2 != 0 {
		t.Fatalf("expected 0%%, got %d", pct2)
	}

	m3 := Meter{Limit: 100, Stale: true}
	_, ok3 := m3.PercentRemaining()
	if ok3 {
		t.Fatal("expected not ok when stale")
	}

	m4 := Meter{Used: 50}
	_, ok4 := m4.PercentRemaining()
	if ok4 {
		t.Fatal("expected not ok when limit unknown")
	}
}
