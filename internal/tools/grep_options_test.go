package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func grepFixture(t *testing.T) *Grep {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n// Needle one\nfunc A() {}\nneedle two\n")
	write("b.py", "x = 1\nNEEDLE = 2\n")
	write("sub/c.go", "package sub\n\nvar v = 1\n// needle three\nvar w = 2\n")
	return &Grep{Root: root, Cwd: NewCwd(root)}
}

// Both backends must honour every argument: ripgrep when installed, POSIX
// grep otherwise.
func grepBackends(t *testing.T, g *Grep) map[string]*Grep {
	t.Helper()
	fallback := *g
	fallback.noRg = true
	out := map[string]*Grep{"grep": &fallback}
	if _, err := exec.LookPath("rg"); err == nil {
		rg := *g
		out["rg"] = &rg
	}
	return out
}

func runGrep(t *testing.T, g *Grep, backend string, args map[string]any) string {
	t.Helper()
	res, err := g.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("%s %v: %v", backend, args, err)
	}
	return res.Content
}

func TestGrepOptions(t *testing.T) {
	for backend, g := range grepBackends(t, grepFixture(t)) {
		t.Run(backend, func(t *testing.T) {
			if got := runGrep(t, g, backend, map[string]any{"pattern": "needle"}); strings.Count(got, "\n")+1 != 2 {
				t.Fatalf("case-sensitive default = %q", got)
			}
			if got := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true}); strings.Count(got, "\n")+1 != 4 {
				t.Fatalf("-i = %q", got)
			}
			files := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true, "output_mode": "files_with_matches"})
			if got := strings.Split(files, "\n"); !slices.Equal(got, []string{"a.go", "b.py", "sub/c.go"}) {
				t.Fatalf("files_with_matches = %q", files)
			}
			count := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true, "output_mode": "count"})
			if !strings.Contains(count, "a.go:2") || strings.Contains(count, ":0") {
				t.Fatalf("count = %q", count)
			}
			if got := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true, "glob": "*.py"}); !strings.HasPrefix(got, "b.py:2:") || strings.Contains(got, ".go") {
				t.Fatalf("glob = %q", got)
			}
			if got := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true, "type": "go", "output_mode": "files_with_matches"}); strings.Contains(got, "b.py") {
				t.Fatalf("type = %q", got)
			}
			if got := runGrep(t, g, backend, map[string]any{"pattern": "needle", "-i": true, "head_limit": 1}); strings.Contains(got, "\n") {
				t.Fatalf("head_limit = %q", got)
			}
			if got := runGrep(t, g, backend, map[string]any{"pattern": "Needle one", "-n": false}); got != "a.go:// Needle one" {
				t.Fatalf("-n false = %q", got)
			}
			ctx := runGrep(t, g, backend, map[string]any{"pattern": "needle three", "-B": 1, "-A": 1})
			for _, want := range []string{"sub/c.go-3-var v = 1", "sub/c.go:4:// needle three", "sub/c.go-5-var w = 2"} {
				if !strings.Contains(ctx, want) {
					t.Fatalf("context output missing %q:\n%s", want, ctx)
				}
			}
		})
	}
}

func TestGrepRejectsBadArguments(t *testing.T) {
	g := grepFixture(t)
	for _, args := range []map[string]any{
		{"pattern": "x", "output_mode": "lines"},
		{"pattern": "x", "head_limit": 0},
		{"pattern": "x", "-A": -1},
	} {
		if _, err := g.Execute(context.Background(), args); err == nil {
			t.Fatalf("%v: want an error", args)
		}
	}
}

func TestGrepFallbackRejectsUnknownType(t *testing.T) {
	if _, err := grepArgs(grepQuery{pattern: "x", fileType: "cobol"}); err == nil {
		t.Fatal("an unknown type must not silently search everything")
	}
}

func TestGrepContextIsBounded(t *testing.T) {
	q, err := (&Grep{}).parseGrepQuery(map[string]any{"pattern": "x", "-C": 500, "head_limit": 99999})
	if err != nil {
		t.Fatal(err)
	}
	if q.after != grepMaxContext || q.before != grepMaxContext || q.limit != grepMaxHeadLimit {
		t.Fatalf("bounds not applied: %+v", q)
	}
}
