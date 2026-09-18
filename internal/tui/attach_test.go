package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/filediff"
	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestParseTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string // raw paths expected (complete tokens only)
	}{
		{"plain file", "read @README.md ", []string{"README.md"}},
		{"quoted spaces", `read @"path with spaces.txt" `, []string{"path with spaces.txt"}},
		{"user@host ignored", "ask user@example.com", nil},
		{"agent reference ignored", "review @agent:security-expert", nil},
		{"trailing incomplete", "read @README.md", nil},
		{"multiple", "compare @a.txt @b.txt ", []string{"a.txt", "b.txt"}},
		{"bare at", "read @", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toks := parseTokens(tc.input)
			var got []string
			for _, tok := range toks {
				if tok.complete {
					got = append(got, tok.raw)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("token %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestAttachmentPreviewForSafeFile(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.attachments = map[int]*attachment{
		1: {id: 1, text: "@doc.md", raw: "doc.md", body: "body", state: attachSafe, diff: filediff.Preview("doc.md", "old", "body")},
	}
	a.attachOrder = []int{1}
	previews, directive := a.attachmentPreviews()
	if len(previews) != 1 {
		t.Fatalf("previews = %d, want 1", len(previews))
	}
	msg := previews[0]
	if msg.Role != "tool" || msg.ToolName != "Read" || msg.Status != "✓" {
		t.Fatalf("preview = %+v, want Read tool row", msg)
	}
	if msg.Content != "body" {
		t.Fatalf("content = %q, want body", msg.Content)
	}
	if p, ok := msg.Meta["path"].(string); !ok || p != "doc.md" {
		t.Fatalf("meta path = %q, want doc.md", p)
	}
	if directive != "" {
		t.Fatalf("directive should be empty for safe attachments, got %q", directive)
	}
}

func TestAttachmentPreviewForRejectedFile(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.attachments = map[int]*attachment{
		1: {id: 1, text: "@doc.md", raw: "doc.md", state: attachRejected, sentinel: rolemanager.SentinelPromptInjection},
	}
	a.attachOrder = []int{1}
	previews, directive := a.attachmentPreviews()
	if len(previews) != 1 {
		t.Fatalf("previews = %d, want 1", len(previews))
	}
	msg := previews[0]
	if msg.Role != "tool" || msg.ToolName != "Read" {
		t.Fatalf("preview = %+v, want Read tool row", msg)
	}
	want := "tool result withheld: attachment @doc.md classified PROMPT_INJECTION"
	if msg.Content != want {
		t.Fatalf("content = %q, want %q", msg.Content, want)
	}
	if directive == "" {
		t.Fatalf("expected a non-empty directive for rejected attachments")
	}
	if !strings.Contains(directive, "@doc.md") || !strings.Contains(directive, "PROMPT_INJECTION") {
		t.Fatalf("directive = %q, want it to name the withheld file and sentinel", directive)
	}
}

func TestAttachmentBodyNotIncludedWhenRejected(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.attachments = map[int]*attachment{
		1: {id: 1, text: "@safe.md", raw: "safe.md", body: "safe body", state: attachSafe},
		2: {id: 2, text: "@bad.md", raw: "bad.md", body: "bad body", state: attachRejected, sentinel: rolemanager.SentinelPromptInjection},
	}
	a.attachOrder = []int{1, 2}
	previews, directive := a.attachmentPreviews()
	if len(previews) != 2 {
		t.Fatalf("previews = %d, want 2", len(previews))
	}
	if previews[0].Content != "safe body" {
		t.Fatalf("safe preview body wrong")
	}
	if strings.Contains(previews[1].Content, "bad body") {
		t.Fatalf("rejected preview must not leak body bytes")
	}
	if directive == "" {
		t.Fatalf("directive should mention the rejected attachment")
	}
}

func TestAttachmentRejectedOnPathEscape(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.editor.SetValue("read @../../AGENTS.md ")
	a.syncAttachments()
	for id, att := range a.attachments {
		t.Logf("attachment %d: text=%q state=%d reason=%q", id, att.text, att.state, att.reason)
	}
	found := false
	for _, att := range a.attachments {
		if att.state == attachRejected && strings.Contains(att.reason, "escapes") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected path escape rejection")
	}
}
