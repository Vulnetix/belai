package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
