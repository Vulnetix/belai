package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/run"
)

func TestSplitHandoffArg(t *testing.T) {
	cases := []struct{ in, path, rest string }{
		{"plan.md", "plan.md", ""},
		{"@docs/plan.md do it", "docs/plan.md", "do it"},
		{`"my plans/p.md" go`, "my plans/p.md", "go"},
		{"  ", "", ""},
	}
	for _, c := range cases {
		p, r := splitHandoffArg(c.in)
		if p != c.path || r != c.rest {
			t.Errorf("splitHandoffArg(%q) = %q, %q; want %q, %q", c.in, p, r, c.path, c.rest)
		}
	}
}

func TestHandoffFactsFrom(t *testing.T) {
	atts := []run.Attachment{
		{Kind: "file", Label: "@notes.txt", Body: "1. a"},
		{Kind: "file", Label: `@"docs/x.md"`, Body: "1. edit `internal/a.go`\n2. test"},
	}
	f := handoffFactsFrom(atts)
	if f == nil || f.Tasks != 2 || f.Label != `docs/x.md` {
		t.Fatalf("facts = %+v", f)
	}
	if handoffFactsFrom(atts[:1]) != nil {
		t.Fatalf("a non-Markdown file must not be a plan")
	}
}

func TestHandoffWithoutPathPrintsUsage(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if cmd := a.startHandoff(""); cmd != nil || a.pendingHandoff {
		t.Fatalf("no path must not start a handoff")
	}
}
