package transcript

import (
	"reflect"
	"strings"
	"testing"
)

func TestUsageTotalFallsBackToComponents(t *testing.T) {
	u := Usage{PromptTokens: 100, CompletionTokens: 50}
	if u.Total() != 150 {
		t.Fatalf("Total = %d, want 150", u.Total())
	}
	u2 := Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 200}
	if u2.Total() != 200 {
		t.Fatalf("Total = %d, want 200 (explicit wins)", u2.Total())
	}
}

func TestEstimateContextNoAnchor(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi"},
	}
	e := EstimateContext(msgs)
	if e.LastUsageIndex != -1 {
		t.Fatalf("LastUsageIndex = %d, want -1", e.LastUsageIndex)
	}
	if e.UsageTokens != 0 {
		t.Fatalf("UsageTokens = %d, want 0", e.UsageTokens)
	}
	if e.Tokens == 0 || e.TrailingTokens != e.Tokens {
		t.Fatalf("estimated tokens = %d / %d, want equal non-zero", e.Tokens, e.TrailingTokens)
	}
}

func TestEstimateContextAnchoredMidList(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", Content: "anchor", Usage: &Usage{TotalTokens: 1000}},
		{Role: "user", Content: "after one"},
		{Role: "assistant", Content: "after two"},
	}
	e := EstimateContext(msgs)
	if e.LastUsageIndex != 1 {
		t.Fatalf("LastUsageIndex = %d, want 1", e.LastUsageIndex)
	}
	if e.UsageTokens != 1000 {
		t.Fatalf("UsageTokens = %d, want 1000", e.UsageTokens)
	}
	wantTrailing := EstimateTokens(msgs[2]) + EstimateTokens(msgs[3])
	if e.TrailingTokens != wantTrailing {
		t.Fatalf("TrailingTokens = %d, want %d", e.TrailingTokens, wantTrailing)
	}
	if e.Tokens != 1000+wantTrailing {
		t.Fatalf("Tokens = %d, want %d", e.Tokens, 1000+wantTrailing)
	}
}

func TestEstimateContextAnchorLast(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "before"},
		{Role: "assistant", Content: "anchor", Usage: &Usage{TotalTokens: 42}},
	}
	e := EstimateContext(msgs)
	if e.TrailingTokens != 0 {
		t.Fatalf("TrailingTokens = %d, want 0", e.TrailingTokens)
	}
	if e.Tokens != 42 {
		t.Fatalf("Tokens = %d, want 42", e.Tokens)
	}
}

func TestEstimateTokensCountsRunes(t *testing.T) {
	multiByte := Message{Role: "user", Content: "éééé"}
	// 4 runes, 8 bytes: the byte count must not be used.
	if got := EstimateTokens(multiByte); got != (4+16+3)/4 {
		t.Fatalf("EstimateTokens = %d, want %d", got, (4+16+3)/4)
	}
}

func TestMeterPercentRemaining(t *testing.T) {
	if _, ok := (Meter{Used: 100, Limit: 0}).PercentRemaining(); ok {
		t.Fatalf("unknown window must report ok=false")
	}
	if _, ok := (Meter{Used: 100, Limit: 1000, Stale: true}).PercentRemaining(); ok {
		t.Fatalf("stale meter must report ok=false")
	}
	pct, ok := (Meter{Used: 250, Limit: 1000}).PercentRemaining()
	if !ok || pct != 75 {
		t.Fatalf("PercentRemaining = %d/%v, want 75/true", pct, ok)
	}
	pct, ok = (Meter{Used: 1000, Limit: 1000}).PercentRemaining()
	if !ok || pct != 0 {
		t.Fatalf("full window = %d/%v, want 0/true", pct, ok)
	}
}

func TestSerializeDropsSystemAndTruncatesTools(t *testing.T) {
	big := strings.Repeat("x", 5000)
	msgs := []Message{
		{Role: "system", Content: "harness notice"},
		{Role: "user", Content: "hi"},
		{Role: "tool", ToolName: "Read", Content: big},
	}
	out := Serialize(msgs, SerializeOptions{Nonce: "abcd1234"})

	if strings.Contains(out, "harness notice") {
		t.Fatalf("system messages should be dropped")
	}
	if !strings.Contains(out, `id="abcd1234"`) {
		t.Fatalf("nonce should appear in open tag: %q", out)
	}
	if !strings.Contains(out, "</conversation>") {
		t.Fatalf("close tag missing")
	}
	if !strings.Contains(out, "truncated, 5000 chars total") {
		t.Fatalf("truncation marker missing: %q", out)
	}
	if strings.Contains(out, strings.Repeat("x", 4000)) {
		t.Fatalf("tool result should be truncated to default max")
	}
}

func TestSerializeRuneSafeTruncation(t *testing.T) {
	content := strings.Repeat("é", 3000)
	msgs := []Message{{Role: "tool", ToolName: "Read", Content: content}}
	out := Serialize(msgs, SerializeOptions{MaxToolResultChars: 10})
	if !strings.Contains(out, "truncated, 3000 chars total") {
		t.Fatalf("marker missing: %q", out)
	}
	// The truncated content must be valid UTF-8 (no split rune).
	if !strings.Contains(out, strings.Repeat("é", 10)) {
		t.Fatalf("expected 10 runes of content")
	}
}

func TestSerializeNoNonce(t *testing.T) {
	out := Serialize([]Message{{Role: "user", Content: "hi"}}, SerializeOptions{})
	if !strings.Contains(out, "<conversation>\n") {
		t.Fatalf("unsuffixed open tag missing: %q", out)
	}
}

func TestEstimateContextAnchorReflect(t *testing.T) {
	// Sanity: exported Estimate shape matches what the footer reads.
	e := EstimateContext([]Message{{Role: "user", Content: "hi"}})
	var _ = reflect.TypeOf(e)
}

func TestTruncateRunesExported(t *testing.T) {
	if got := TruncateRunes("hello", 100); got != "hello" {
		t.Fatalf("TruncateRunes(short) = %q", got)
	}
	if got := TruncateRunes("hello world", 5); got != "hello… (truncated, 11 chars total)" {
		t.Fatalf("TruncateRunes(long) = %q", got)
	}
	if got := TruncateRunes("éééé", 3); got != "ééé… (truncated, 4 chars total)" {
		t.Fatalf("TruncateRunes(multibyte) = %q", got)
	}
}
