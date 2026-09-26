package filelib

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func testSpec(t *testing.T) (Spec, string) {
	t.Helper()
	workdir := t.TempDir()
	s := Spec{
		Ext:        ".md",
		TempPrefix: ".tmp-prompt-",
		GlobalDir:  config.GlobalPromptsDir,
		ProjectDir: config.ProjectPromptsDir,
	}
	return s, workdir
}

func TestSlugErrorCases(t *testing.T) {
	for _, in := range []string{"", "   ", "---", "___", "!!!", "\t\n"} {
		if _, err := Slug(in); err == nil {
			t.Errorf("Slug(%q) should fail", in)
		}
	}
	long := strings.Repeat("a", maxSlugRunes+1)
	if _, err := Slug(long); err == nil {
		t.Fatalf("Slug(%d runes) should fail", maxSlugRunes+1)
	}
	// Exactly maxSlugRunes is fine.
	if got, err := Slug(strings.Repeat("a", maxSlugRunes)); err != nil || got != strings.Repeat("a", maxSlugRunes) {
		t.Fatalf("Slug(max) = (%q, %v)", got, err)
	}
}

func TestSlugUnderscoreBecomesHyphen(t *testing.T) {
	cases := map[string]string{
		"deploy app":    "deploy-app",
		"Deploy_App":    "deploy-app",
		"  spaced out ": "spaced-out",
		"a..b":          "a-b",
		"multi--dash":   "multi-dash",
	}
	for in, want := range cases {
		if got, err := Slug(in); err != nil || got != want {
			t.Fatalf("Slug(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
}

func TestDirUnknownScope(t *testing.T) {
	s, workdir := testSpec(t)
	if _, err := s.Dir(config.Scope("bogus"), workdir); err == nil {
		t.Fatal("Dir(bogus) should fail")
	}
}

func TestRenumberStep(t *testing.T) {
	cases := []struct {
		n    int
		want int
	}{
		{1, 10},
		{99, 10},
		{100, 9},
		{200, 4},
		{999, 1},
		{1000, 1},
		{10000, 1},
	}
	for _, c := range cases {
		if got := renumberStep(c.n); got != c.want {
			t.Errorf("renumberStep(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

func TestRenumberRewritesOrdersAndFiles(t *testing.T) {
	s, workdir := testSpec(t)
	dir := filepath.Join(workdir, ".vulnetix", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var entries []Entry
	for i, name := range []string{"a", "b", "c"} {
		path := filepath.Join(dir, s.FileName((i+1)*10, name, true))
		if err := os.WriteFile(path, []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, Entry{Name: name, Body: name, Order: (i + 1) * 10, Enabled: true, Scope: config.ScopeProject, Path: path})
	}
	moved, err := s.renumber(config.ScopeProject, workdir, entries, 7)
	if err != nil {
		t.Fatalf("renumber: %v", err)
	}
	for i, e := range moved {
		want := (i + 1) * 7
		if e.Order != want {
			t.Fatalf("moved[%d].Order = %d, want %d", i, e.Order, want)
		}
		if _, err := os.Stat(e.Path); err != nil {
			t.Fatalf("renamed file %s missing: %v", e.Path, err)
		}
	}
}

func TestRenumberCollisionRollsBack(t *testing.T) {
	s, workdir := testSpec(t)
	dir := filepath.Join(workdir, ".vulnetix", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Place a source file and pre-create its final target so renumber collides.
	src := filepath.Join(dir, s.FileName(10, "a", true))
	if err := os.WriteFile(src, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{Name: "a", Body: "a", Order: 10, Enabled: true, Scope: config.ScopeProject, Path: src}}
	// The step-1 final target of "a" is 001-a.md; create it to force collision.
	if err := os.WriteFile(filepath.Join(dir, s.FileName(1, "a", true)), []byte("blocker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.renumber(config.ScopeProject, workdir, entries, 1); err == nil {
		t.Fatal("renumber should collide")
	}
	// Source must have been restored to its original name.
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source not restored: %v", err)
	}
}

func TestSetEnabledCollisionRefused(t *testing.T) {
	s, workdir := testSpec(t)
	dir := filepath.Join(workdir, ".vulnetix", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e := Entry{Name: "a", Order: 10, Enabled: true, Scope: config.ScopeProject, Path: filepath.Join(dir, s.FileName(10, "a", true))}
	if err := os.WriteFile(e.Path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pre-create the disabled target so the rename would collide.
	if err := os.WriteFile(filepath.Join(dir, s.FileName(10, "a", false)), []byte("blocker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnabled(e, false); err == nil {
		t.Fatal("SetEnabled should refuse to overwrite an existing target")
	}
	// No-op toggle with the same state returns the entry unchanged.
	if got, err := s.SetEnabled(e, true); err != nil || got.Path != e.Path {
		t.Fatalf("no-op SetEnabled = (%+v, %v)", got, err)
	}
}

func TestReorderIndexOutOfRange(t *testing.T) {
	s, workdir := testSpec(t)
	entries := []Entry{{Name: "a"}}
	for _, from := range []int{-1, 1} {
		if _, err := s.Reorder(config.ScopeProject, workdir, entries, from, 0); err == nil {
			t.Fatalf("Reorder(from=%d) should fail", from)
		}
	}
	for _, to := range []int{-1, 1} {
		if _, err := s.Reorder(config.ScopeProject, workdir, entries, 0, to); err == nil {
			t.Fatalf("Reorder(to=%d) should fail", to)
		}
	}
}

func TestCreateRenumbersWhenGridOverflows(t *testing.T) {
	s, workdir := testSpec(t)
	// 99 entries occupy 010..990; the 100th would land at 1000 and must
	// trigger a renumber onto a tighter grid.
	for i := 0; i < 99; i++ {
		name := string(rune('a'+i%26)) + string(rune('a'+i/26)) + string(rune('0'+i%10))
		if _, err := s.Create(config.ScopeProject, workdir, name, name); err != nil {
			t.Fatalf("create %d (%s): %v", i, name, err)
		}
	}
	e, err := s.Create(config.ScopeProject, workdir, "overflow-entry", "x")
	if err != nil {
		t.Fatalf("create overflow entry: %v", err)
	}
	if e.Order > 999 {
		t.Fatalf("order = %d, want <= 999", e.Order)
	}
	listing, err := s.Load(config.ScopeProject, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 100 {
		t.Fatalf("loaded %d entries, want 100", len(listing.Entries))
	}
	// Every order must be strictly increasing and in 1..999.
	prev := 0
	for _, ent := range listing.Entries {
		if ent.Order <= prev || ent.Order > 999 {
			t.Fatalf("bad order sequence: %d after %d", ent.Order, prev)
		}
		prev = ent.Order
	}
}

func TestReadEntryTrimsExactlyOneNewlineAndCR(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "e.md")
	cases := []struct {
		in   string
		want string
	}{
		{"body\n", "body"},
		{"body\r\n", "body"},
		{"body\n\n", "body\n"}, // only one trailing newline trimmed
		{"body", "body"},
		{"\n", ""},
	}
	for _, c := range cases {
		if err := os.WriteFile(path, []byte(c.in), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readEntry(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Fatalf("readEntry(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenameAllMismatchedLengths(t *testing.T) {
	dir := t.TempDir()
	src1 := filepath.Join(dir, "s1")
	src2 := filepath.Join(dir, "s2")
	dst := filepath.Join(dir, "d")
	for _, p := range []string{src1, src2} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Fewer dsts than srcs: only the first src is renamed.
	if err := renameAll([]string{src1, src2}, []string{dst}); err != nil {
		t.Fatalf("renameAll: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst missing: %v", err)
	}
	if _, err := os.Stat(src2); err != nil {
		t.Fatalf("src2 should be untouched: %v", err)
	}
	// A non-existent source stops the batch and returns the error.
	if err := renameAll([]string{filepath.Join(dir, "nope"), src2}, []string{dst, filepath.Join(dir, "d2")}); err == nil {
		t.Fatal("renameAll with missing source should error")
	}
}

func TestEnabledPreservesOrder(t *testing.T) {
	entries := []Entry{
		{Name: "a", Enabled: true, Order: 10},
		{Name: "b", Enabled: false, Order: 20},
		{Name: "c", Enabled: true, Order: 30},
	}
	got := Enabled(entries)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("Enabled = %+v", got)
	}
}

func TestLoadReadErrorPropagates(t *testing.T) {
	s, workdir := testSpec(t)
	dir := filepath.Join(workdir, ".vulnetix", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A validly-named regular file that cannot be read must surface an error
	// rather than silently loading an empty body.
	path := filepath.Join(dir, s.FileName(10, "a", true))
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)
	if _, err := s.Load(config.ScopeProject, workdir); err == nil {
		t.Fatal("expected read error for an unreadable entry")
	} else if errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected not-exist error")
	}
}
