package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestExitCardRendersFactsAndResume(t *testing.T) {
	c := ExitCard{
		Name:      "refactor-the-parser",
		SessionID: "d4f1a9c2e3b4a1f0",
		ResumeArg: "d4f1a9c2",
		Turns:     12,
		Duration:  "8m 41s",
		Tokens:    "~34.2k tokens",
		Model:     "claude-opus-4",
		Provider:  "anthropic",
		Path:      "~/.vulnetix/belai/sessions/x/d4f1a9c2.jsonl",
	}
	v := c.View()
	for _, want := range []string{"session ended", "refactor-the-parser", "d4f1a9c2", "12 turns", "8m 41s", "~34.2k tokens", "anthropic/claude-opus-4", "belai --resume", "d4f1a9c2.jsonl"} {
		if !strings.Contains(v, want) {
			t.Fatalf("exit card missing %q:\n%s", want, v)
		}
	}
}

func TestExitCardASCIIHasNoHalfBlocks(t *testing.T) {
	c := ExitCard{Name: "n", SessionID: "abcdef123456", ResumeArg: "abcdef12", Turns: 1, Path: "/p/s.jsonl"}
	v := c.textView()
	if strings.Contains(v, "▀") {
		t.Fatalf("ASCII exit card must not contain half-blocks: %q", v)
	}
	for _, want := range []string{"BELAI · session ended", "belai --resume abcdef12", "/p/s.jsonl"} {
		if !strings.Contains(v, want) {
			t.Fatalf("ASCII exit card missing %q: %q", want, v)
		}
	}
}

func TestExitCardHeightIsSixRows(t *testing.T) {
	without := ExitCard{SessionID: "d4f1a9c2e3b4a1f0", ResumeArg: "d4f1a9c2"}.pixView()
	with := ExitCard{
		Name: "n", SessionID: "d4f1a9c2e3b4a1f0", ResumeArg: "d4f1a9c2",
		Turns: 1, Duration: "1s", Tokens: "10 tokens", Model: "m", Provider: "p",
		Path: "/p/x.jsonl",
	}.pixView()
	if got := lipgloss.Height(without); got != 6 {
		t.Fatalf("expected 6-row exit card without facts, got %d", got)
	}
	if got := lipgloss.Height(with); got != 6 {
		t.Fatalf("expected 6-row exit card with facts, got %d", got)
	}
}

func TestPickTipIsStable(t *testing.T) {
	if PickTip("seed-a") != PickTip("seed-a") {
		t.Fatal("tip must be stable for a fixed seed")
	}
	if PickTip("") != "" {
		t.Fatal("empty seed must return empty tip")
	}
}

func TestBannerTipReplacesHelpLine(t *testing.T) {
	b := Banner{Width: 80, Tip: "ctrl+x copies the session id"}
	v := b.pixView()
	if !strings.Contains(v, "ctrl+x copies the session id") {
		t.Fatalf("banner should render its tip: %q", v)
	}
	if strings.Contains(v, "type ") && strings.Contains(v, "/help") {
		t.Fatalf("banner should not render the default /help line when Tip is set: %q", v)
	}
}

func TestBannerResumedVariant(t *testing.T) {
	b := Banner{Width: 80, Resumed: "refactor-the-parser", RestoredTurns: 7}
	v := b.pixView()
	if !strings.Contains(v, "resumed refactor-the-parser") {
		t.Fatalf("banner should show the resumed name: %q", v)
	}
	if !strings.Contains(v, "7 turns restored") {
		t.Fatalf("banner should show the restored turn count: %q", v)
	}
	if strings.Contains(v, "a safer coding harness") {
		t.Fatalf("resumed banner should replace the default subtitle: %q", v)
	}
}
