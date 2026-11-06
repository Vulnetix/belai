package finishreview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/workdiff"
)

func TestBaselineRoundTripIsPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "g1.json")
	in := Baseline{Complete: true, Paths: []string{"a.txt", "b.txt"}}
	if err := in.Save(file); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("baseline mode = %o, want 600", st.Mode().Perm())
	}
	if d, _ := os.Stat(filepath.Dir(file)); d.Mode().Perm() != 0o700 {
		t.Fatalf("baseline directory mode = %o, want 700", d.Mode().Perm())
	}
	out := LoadBaseline(file)
	if !out.Complete || !out.Has("a.txt") || !out.Has("b.txt") || out.Has("c.txt") {
		t.Fatalf("round trip lost paths: %+v", out)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".baseline-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

// A baseline that cannot be trusted is the empty one, which is the stricter
// reading: every untracked file counts as new.
func TestBaselineLoadFailsTowardsStrict(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := map[string]string{
		"garbage":     "not json",
		"wrong ver":   `{"version":9,"complete":true,"paths":["a"]}`,
		"incomplete":  `{"version":1,"complete":false,"paths":["a"]}`,
		"empty path":  `{"version":1,"complete":true,"paths":[""]}`,
		"long path":   `{"version":1,"complete":true,"paths":["` + strings.Repeat("a", 401) + `"]}`,
		"too many":    `{"version":1,"complete":true,"paths":[` + strings.TrimSuffix(strings.Repeat(`"x",`, workdiff.MaxFiles+1), ",") + `]}`,
		"empty input": ``,
	}
	for name, body := range cases {
		b := LoadBaseline(write(strings.ReplaceAll(name, " ", "_"), body))
		if b.Complete || b.Has("a") {
			t.Errorf("%s: trusted a bad baseline: %+v", name, b)
		}
	}
	if b := LoadBaseline(filepath.Join(dir, "missing")); b.Complete {
		t.Error("a missing baseline was trusted")
	}
	big := write("big", strings.Repeat("x", maxBaselineBytes+1))
	if b := LoadBaseline(big); b.Complete {
		t.Error("an oversized baseline was read")
	}
}

func TestBaselineRefusesSymbolicLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "g.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := (Baseline{Complete: true}).Save(link); err == nil {
		t.Fatal("saved through a symbolic link")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("the link target was written: %q", b)
	}
	if b := LoadBaseline(link); b.Complete {
		t.Fatal("read through a symbolic link")
	}
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "linkdir")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := (Baseline{Complete: true}).Save(filepath.Join(linkDir, "g.json")); err == nil {
		t.Fatal("saved into a symbolic-link directory")
	}
}

func TestCaptureIsStrictWhenTheTreeIsNotWhole(t *testing.T) {
	if b := baselineOf(workdiff.Snapshot{More: 3}); b.Complete {
		t.Error("a truncated snapshot made a complete baseline")
	}
	if b := baselineOf(workdiff.Snapshot{Unavailable: "x"}); b.Complete {
		t.Error("an unreadable tree made a complete baseline")
	}
	snap := workdiff.Snapshot{Files: []workdiff.File{{Path: "z.txt"}, {Path: "a.txt"}, {Path: ".vulnetix/x"}}}
	b := baselineOf(snap)
	if !b.Complete || len(b.Paths) != 2 || b.Paths[0] != "a.txt" || b.Has(".vulnetix/x") {
		t.Fatalf("baseline = %+v", b)
	}
	dir := repo(t)
	write(t, dir, "mine.txt", "x")
	got := Capture(context.Background(), dir, nil)
	if !got.Has("mine.txt") {
		t.Fatalf("capture missed a file: %+v", got)
	}
}
