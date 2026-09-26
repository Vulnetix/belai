package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// A directory attached with @ is listed, not read: the validation command
// must admit it in-process (no provider round trip) and the transcript row
// must be an Ls tool row, not a Read row that withheld as MALFORMED.
func TestAttachmentDirectoryLists(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "docs", "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.editor.SetValue("list @docs/ ")
	cmd := a.syncAttachments()
	if cmd == nil {
		t.Fatal("syncAttachments returned no validation command")
	}
	a.Update(cmd())

	if len(a.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(a.attachments))
	}
	for _, att := range a.attachments {
		if att.state != attachSafe {
			t.Fatalf("directory attachment state = %d (reason %q), want safe", att.state, att.reason)
		}
		if !att.isDir {
			t.Fatal("directory attachment must be marked isDir")
		}
		if !strings.Contains(att.body, "a.md") || !strings.Contains(att.body, "sub/") {
			t.Fatalf("listing missing entries:\n%s", att.body)
		}
	}

	previews, directive := a.attachmentPreviews()
	if len(previews) != 1 {
		t.Fatalf("previews = %d, want 1", len(previews))
	}
	if previews[0].ToolName != "Ls" {
		t.Fatalf("preview tool = %q, want Ls", previews[0].ToolName)
	}
	if previews[0].Status != "✓" {
		t.Fatalf("preview status = %q, want ✓", previews[0].Status)
	}
	if !strings.Contains(previews[0].Content, "a.md") {
		t.Fatalf("preview content missing listing:\n%s", previews[0].Content)
	}
	if directive != "" {
		t.Fatalf("a safe directory must not raise a directive, got %q", directive)
	}
}

// An empty directory still gets a row and a body: a zero-byte attachment
// would be dropped from the model's turn while the transcript promised a
// listing.
func TestAttachmentEmptyDirectoryLists(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	a.editor.SetValue("list @empty/ ")
	cmd := a.syncAttachments()
	a.Update(cmd())

	for _, att := range a.attachments {
		if att.state != attachSafe {
			t.Fatalf("empty directory state = %d (reason %q), want safe", att.state, att.reason)
		}
		if att.body != "(empty directory)\n" {
			t.Fatalf("empty directory body = %q, want the explicit marker", att.body)
		}
	}
}

// Subdirectories are marked with a trailing slash, so the model can tell
// where to point a follow-up @token without a second round trip.
func TestAttachmentDirectoryListingMarksSubdirs(t *testing.T) {
	got, err := listDirForAttachment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != "(empty directory)\n" {
		t.Fatalf("empty listing = %q", got)
	}

	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"adir", "zdir"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err = listDirForAttachment(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "a.md\nadir/\nb.txt\nzdir/\n"
	if got != want {
		t.Fatalf("listing = %q, want %q", got, want)
	}
}

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
	want := "tool result withheld: attachment @doc.md classified " + rolemanager.SentinelPromptInjection.Label()
	if msg.Content != want {
		t.Fatalf("content = %q, want %q", msg.Content, want)
	}
	if directive == "" {
		t.Fatalf("expected a non-empty directive for rejected attachments")
	}
	label := rolemanager.SentinelPromptInjection.Label()
	if !strings.Contains(directive, "@doc.md") || !strings.Contains(directive, label) {
		t.Fatalf("directive = %q, want it to name the withheld file and sentinel label %q", directive, label)
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

// An attachment whose path lands inside an added workspace directory is
// resolved and read against that root, not rejected as outside the primary
// workdir.
func TestAttachmentResolvesInWorkspaceDir(t *testing.T) {
	workdir := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "extra.txt"), []byte("extra content"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := New(Options{Workdir: workdir})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	// Guardrails off so file bytes are admitted without a real classifier
	// round trip in this test.
	off := false
	a.guardrailsOverride = &off
	a.syncPosture()
	a.workspaceDirs = []string{extra}
	t.Logf("workspaceDirs = %v", a.workspaceDirs)
	raw := filepath.Join(extra, "extra.txt")
	t.Logf("raw = %q", raw)
	root, rel, err := a.resolveAttachmentPath(raw)
	t.Logf("resolveAttachmentPath = root=%q rel=%q err=%v", root, rel, err)
	a.editor.SetValue("read @" + raw + " ")
	cmd := a.syncAttachments()
	if cmd == nil {
		for id, att := range a.attachments {
			t.Logf("att %d: text=%q state=%d reason=%q", id, att.text, att.state, att.reason)
		}
		t.Fatal("syncAttachments returned no validation command")
	}
	a.Update(cmd())

	for _, att := range a.attachments {
		if att.state != attachSafe {
			t.Fatalf("attachment state = %d (reason %q), want safe", att.state, att.reason)
		}
		if att.body != "extra content" {
			t.Fatalf("attachment body = %q, want extra content", att.body)
		}
		if att.root != extra {
			t.Fatalf("attachment root = %q, want %q", att.root, extra)
		}
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
