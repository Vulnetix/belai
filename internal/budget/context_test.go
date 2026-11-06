package budget

import (
	"reflect"
	"testing"

	"github.com/vulnetix/belai/internal/transcript"
)

// TestBudgetRule25_ContextBreakdown pins the arithmetic of /context: the parts
// sum to used, the share is clamped to 0..100, the largest tool results are
// listed by size then name, and no window means no gauge and no free row.
func TestBudgetRule25_ContextBreakdown(t *testing.T) {
	b := Breakdown(1000, 2000, 3000, map[string]int{"Read": 400, "Bash": 900, "Grep": 400, "A": 1, "B": 2, "C": 3, "D": 4}, 20000)
	if b.ToolResults != 1710 || b.Used != 7710 {
		t.Fatalf("totals = %d / %d", b.ToolResults, b.Used)
	}
	if !b.HasWindow || b.Window != 20000 || b.Percent != 38 || b.Free != 12290 {
		t.Fatalf("window figures = %+v", b)
	}
	want := []ToolSize{{"Bash", 900}, {"Grep", 400}, {"Read", 400}, {"D", 4}, {"C", 3}}
	if !reflect.DeepEqual(b.Top, want) {
		t.Fatalf("top = %v, want %v", b.Top, want)
	}

	over := Breakdown(30000, 0, 0, nil, 20000)
	if over.Percent != 100 || over.Free != 0 {
		t.Fatalf("over the window = %+v", over)
	}

	none := Breakdown(10, 20, 30, map[string]int{"Read": 5}, 0)
	if none.HasWindow || none.Percent != 0 || none.Free != 0 || none.Window != 0 || none.Used != 65 {
		t.Fatalf("no window = %+v", none)
	}

	neg := Breakdown(-5, -1, 7, map[string]int{"Read": -3, "Bash": 0}, -10)
	if neg.Used != 7 || len(neg.Top) != 0 || neg.HasWindow {
		t.Fatalf("negative sizes = %+v", neg)
	}
}

// TestBudgetEdge33_UnknownPriceAndNoWindow pins the failure rule of the reply
// cost line and /context: a model with no price is not costed, and a request
// with no window reports no gauge, never a fabricated number.
func TestBudgetEdge33_UnknownPriceAndNoWindow(t *testing.T) {
	u := transcript.Usage{PromptTokens: 1000, CompletionTokens: 100}
	if _, known := CostFor("someprovider", "no-such-model-xyz", nil, u); known {
		t.Fatal("an unpriced model must not be costed")
	}
	if cost, known := CostFor("ollama", "llama3", nil, u); !known || cost != 0 {
		t.Fatalf("a local model is free, got %d %v", cost, known)
	}
	if b := Breakdown(100, 100, 100, nil, 0); b.HasWindow || b.Free != 0 || b.Percent != 0 {
		t.Fatalf("an unknown window drew a gauge: %+v", b)
	}
	var zero ContextBreakdown
	if got := Breakdown(0, 0, 0, nil, 0); !reflect.DeepEqual(got, zero) {
		t.Fatalf("empty request = %+v", got)
	}
}
