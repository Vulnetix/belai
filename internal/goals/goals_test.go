package goals

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
)

func TestGoalMemoriseLoadRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	g := Goal{Name: "ship-signet", Content: "build and release signet"}

	path, err := Memorise(workdir, g)
	if err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	if path == "" {
		t.Fatalf("Memorise returned empty path")
	}

	got, err := Load(workdir, "ship-signet")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "ship-signet" || got.Content != g.Content {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestGoalNamesForAutocomplete(t *testing.T) {
	workdir := t.TempDir()
	for _, n := range []string{"beta", "alpha"} {
		if _, err := Memorise(workdir, Goal{Name: n, Content: n}); err != nil {
			t.Fatalf("Memorise(%s): %v", n, err)
		}
	}
	names, err := Names(workdir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("names = %v, want [alpha beta]", names)
	}
}

func TestGoalNamesEmpty(t *testing.T) {
	names, err := Names(t.TempDir())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no goals, got %v", names)
	}
}

// Goal names become filenames, so they are slugged before they touch the
// filesystem: anything outside [a-zA-Z0-9._-] collapses to "_", and leading or
// trailing ".", "_" and "-" are trimmed. A name that slugs to nothing is
// rejected rather than written, so a goal can never escape the goals dir.
func TestGoalNameSlugging(t *testing.T) {
	workdir := t.TempDir()
	cases := []struct {
		name, want string
	}{
		{"ship the thing", "ship_the_thing"},
		{"Fix CI/CD", "Fix_CI_CD"},
		{"...leading", "leading"},
		{"trailing---", "trailing"},
		{"keep.dots-and_underscores", "keep.dots-and_underscores"},
	}
	for _, tc := range cases {
		path, err := Memorise(workdir, Goal{Name: tc.name, Content: "body"})
		if err != nil {
			t.Fatalf("Memorise(%q): %v", tc.name, err)
		}
		if got := filepath.Base(path); got != tc.want+".md" {
			t.Errorf("Memorise(%q) wrote %q, want %q", tc.name, got, tc.want+".md")
		}
		// The slug is the identity: Load takes the original name and must find
		// the same file.
		g, err := Load(workdir, tc.name)
		if err != nil {
			t.Fatalf("Load(%q): %v", tc.name, err)
		}
		if g.Content != "body" {
			t.Errorf("Load(%q) content = %q", tc.name, g.Content)
		}
	}
}

// A name with nothing usable in it is refused by both Memorise and Load, and
// refusing means no file is created.
func TestGoalNameRejectsEmptySlug(t *testing.T) {
	workdir := t.TempDir()
	for _, name := range []string{"", "///", "...", "___", "---", "..."} {
		if _, err := Memorise(workdir, Goal{Name: name, Content: "x"}); err == nil {
			t.Errorf("Memorise(%q) must be refused", name)
		}
		if _, err := Load(workdir, name); err == nil {
			t.Errorf("Load(%q) must be refused", name)
		}
	}
	names, err := Names(workdir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("a refused name must write nothing, got %v", names)
	}
}

// A path-traversal attempt is slugged, not honoured: the separators collapse
// and the file lands inside the goals dir like any other.
func TestGoalNameCannotTraverse(t *testing.T) {
	workdir := t.TempDir()
	path, err := Memorise(workdir, Goal{Name: "../../etc/passwd", Content: "x"})
	if err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	dir := config.ProjectGoalsDir(workdir)
	if filepath.Dir(path) != dir {
		t.Fatalf("goal escaped the goals dir: %q not in %q", path, dir)
	}
	if strings.Contains(filepath.Base(path), "/") || strings.Contains(filepath.Base(path), "..") {
		t.Fatalf("traversal survived slugging: %q", path)
	}
}

// Memorise replaces an existing goal of the same name in place rather than
// accumulating copies.
func TestGoalMemoriseOverwrites(t *testing.T) {
	workdir := t.TempDir()
	if _, err := Memorise(workdir, Goal{Name: "same", Content: "first"}); err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	if _, err := Memorise(workdir, Goal{Name: "same", Content: "second"}); err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	g, err := Load(workdir, "same")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if g.Content != "second" {
		t.Fatalf("content = %q, want the later write", g.Content)
	}
	names, err := Names(workdir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 1 {
		t.Fatalf("names = %v, want one entry", names)
	}
}

// Names lists only .md files and never directories, so a stray file in the
// goals dir cannot show up in autocomplete as a goal.
func TestGoalNamesIgnoresNonGoalFiles(t *testing.T) {
	workdir := t.TempDir()
	if _, err := Memorise(workdir, Goal{Name: "real", Content: "x"}); err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	dir := config.ProjectGoalsDir(workdir)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "subdir.md"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	names, err := Names(workdir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 1 || names[0] != "real" {
		t.Fatalf("names = %v, want [real]", names)
	}
}

// A goal that was never written reports an error rather than an empty goal
// that would read as a real, blank objective.
func TestGoalLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir(), "never-written"); err == nil {
		t.Fatal("loading an absent goal must error")
	}
}
