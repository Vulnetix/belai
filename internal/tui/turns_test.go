package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestNormaliseTurnsMergesAdjacentUsers(t *testing.T) {
	in := []run.Turn{
		{Role: "user", Content: "one", Attachments: []run.Attachment{{Label: "a1"}}},
		{Role: "user", Content: "two", Directive: "direct", Attachments: []run.Attachment{{Label: "a2"}}},
		{Role: "assistant", Content: "ok"},
	}
	got := normaliseTurns(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Content != "one\n\ntwo" {
		t.Fatalf("merged content = %q", got[0].Content)
	}
	// The last turn's Attachments and Directive win.
	if len(got[0].Attachments) != 1 || got[0].Attachments[0].Label != "a2" {
		t.Fatalf("attachments = %+v", got[0].Attachments)
	}
	if got[0].Directive != "direct" {
		t.Fatalf("directive = %q", got[0].Directive)
	}
}

func TestNormaliseTurnsDropsEmptyAssistantAndOrphanTool(t *testing.T) {
	in := []run.Turn{
		{Role: "user", Content: "x"},
		{Role: "assistant", Content: "   "},
		{Role: "assistant", Content: "real"},
		{Role: "tool", Content: "no call id"},
	}
	got := normaliseTurns(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[1].Role != "assistant" || got[1].Content != "real" {
		t.Fatalf("got %+v", got)
	}
}

func TestBuildTurnsNormalisesTrailingDanglingUser(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5"})
	a.messages = []components.Message{
		{Role: "user", Content: "first"},
		{Role: "user", Content: "second (dangling reply never landed)"},
	}
	turns := a.buildTurns()
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1: %+v", len(turns), turns)
	}
	if turns[0].Content != "first\n\nsecond (dangling reply never landed)" {
		t.Fatalf("merged = %q", turns[0].Content)
	}
}
