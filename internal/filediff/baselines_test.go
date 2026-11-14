package filediff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// baselineRepo is gitRepo with the baseline capture on.
func baselineRepo(t *testing.T) (string, *Recorder) {
	t.Helper()
	dir := gitRepo(t)
	r := NewRecorder(dir)
	r.Baselines = true
	return dir, r
}

func baselinesAround(t *testing.T, r *Recorder, command string, fn func()) BaselineSet {
	t.Helper()
	snap := r.Before(context.Background(), command)
	fn()
	return snap.Baselines(context.Background())
}

func byBase(set BaselineSet) map[string]Baseline {
	out := map[string]Baseline{}
	for _, b := range set.Items {
		out[filepath.Base(b.Path)] = b
	}
	return out
}

func TestBaselinesCleanTrackedFileComesFromTheIndex(t *testing.T) {
	dir, r := baselineRepo(t)
	set := baselinesAround(t, r, "sed -i s/main/run/ tracked.go", func() {
		write(t, filepath.Join(dir, "tracked.go"), "package main\n\nfunc run() {}\n")
	})
	b, ok := byBase(set)["tracked.go"]
	if !ok || !b.Existed || !b.Known || string(b.Body) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("set = %+v", set)
	}
	if !filepath.IsAbs(b.Path) {
		t.Fatalf("path %q is not absolute", b.Path)
	}
}

func TestBaselinesFileTheCommandCreatedIsAbsent(t *testing.T) {
	dir, r := baselineRepo(t)
	set := baselinesAround(t, r, "touch new.txt", func() {
		write(t, filepath.Join(dir, "new.txt"), "x")
	})
	b, ok := byBase(set)["new.txt"]
	if !ok || b.Existed {
		t.Fatalf("set = %+v, want new.txt recorded as not existing before", set)
	}
}

func TestBaselinesDeletedFileKeepsItsModeFromGit(t *testing.T) {
	dir, r := baselineRepo(t)
	exe := filepath.Join(dir, "run.sh")
	write(t, exe, "#!/bin/sh\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--no-gpg-sign", "-qm", "script")
	set := baselinesAround(t, r, "rm run.sh", func() { _ = os.Remove(exe) })
	b, ok := byBase(set)["run.sh"]
	if !ok || !b.Known || b.Mode != 0o755 || string(b.Body) != "#!/bin/sh\n" {
		t.Fatalf("set = %+v", set)
	}
}

func TestBaselinesAlreadyDirtyAndUntrackedFilesAreReadBeforeTheCommand(t *testing.T) {
	dir, r := baselineRepo(t)
	write(t, filepath.Join(dir, "tracked.go"), "dirty before\n")
	write(t, filepath.Join(dir, "scratch.txt"), "untracked before\n")
	set := baselinesAround(t, r, "rewrite all", func() {
		write(t, filepath.Join(dir, "tracked.go"), "dirty after\n")
		_ = os.Remove(filepath.Join(dir, "scratch.txt"))
	})
	got := byBase(set)
	if b := got["tracked.go"]; !b.Known || string(b.Body) != "dirty before\n" {
		t.Fatalf("tracked.go = %+v", b)
	}
	if b := got["scratch.txt"]; !b.Known || string(b.Body) != "untracked before\n" {
		t.Fatalf("scratch.txt = %+v", b)
	}
}

func TestBaselinesSkipAFileWhoseBytesDidNotChange(t *testing.T) {
	dir, r := baselineRepo(t)
	write(t, filepath.Join(dir, "tracked.go"), "dirty\n")
	set := baselinesAround(t, r, "git add", func() {
		gitIn(t, dir, "add", "tracked.go")
	})
	if len(set.Items) != 0 {
		t.Fatalf("staging alone listed %+v", set.Items)
	}
}

func TestBaselinesOversizeEarlierFileIsNamedNotCaptured(t *testing.T) {
	dir, r := baselineRepo(t)
	r.BaselineMaxBytes = 10
	write(t, filepath.Join(dir, "tracked.go"), strings.Repeat("a", 50))
	set := baselinesAround(t, r, "cmd", func() {
		write(t, filepath.Join(dir, "tracked.go"), "b")
	})
	b := byBase(set)["tracked.go"]
	if b.Known || b.Why == "" || !b.Existed {
		t.Fatalf("tracked.go = %+v, want an unknown earlier state with a reason", b)
	}
}

func TestBaselinesBudgetStopsCapturingMoreThanItMay(t *testing.T) {
	dir, r := baselineRepo(t)
	r.BaselineBudget = 30
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		write(t, filepath.Join(dir, n), strings.Repeat("x", 20))
	}
	set := baselinesAround(t, r, "cmd", func() {
		for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
			write(t, filepath.Join(dir, n), "y")
		}
	})
	known := 0
	for _, b := range set.Items {
		if b.Known {
			known++
		}
	}
	if known != 1 || len(set.Items) != 3 {
		t.Fatalf("known = %d of %d, want exactly one under a 30 byte budget", known, len(set.Items))
	}
}

func TestBaselinesNothingChangedIsEmpty(t *testing.T) {
	_, r := baselineRepo(t)
	if set := baselinesAround(t, r, "ls", func() {}); len(set.Items) != 0 || set.Unavailable != "" {
		t.Fatalf("set = %+v", set)
	}
}

func TestBaselinesAreOffUnlessAskedFor(t *testing.T) {
	dir := gitRepo(t)
	r := NewRecorder(dir)
	write(t, filepath.Join(dir, "tracked.go"), "dirty\n")
	snap := r.Before(context.Background(), "x")
	if snap.bodies != nil {
		t.Fatal("the diff-only recorder read file bodies for baselines")
	}
}

func TestBaselinesPathsAboveTheWorkdirAreCountedNotListed(t *testing.T) {
	dir := gitRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(sub)
	r.Baselines = true
	set := baselinesAround(t, r, "cmd", func() {
		write(t, filepath.Join(dir, "tracked.go"), "changed above\n")
		write(t, filepath.Join(sub, "inside.txt"), "in\n")
	})
	if set.Outside != 1 || len(set.Items) != 1 || filepath.Base(set.Items[0].Path) != "inside.txt" {
		t.Fatalf("set = %+v", set)
	}
}

func TestBaselinesHeuristicOutsideGit(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	r.Baselines = true
	write(t, filepath.Join(dir, "out.txt"), "before\n")
	set := baselinesAround(t, r, "echo after > out.txt && echo n > made.txt", func() {
		write(t, filepath.Join(dir, "out.txt"), "after\n")
	})
	b, ok := byBase(set)["out.txt"]
	if !ok || !b.Known || string(b.Body) != "before\n" {
		t.Fatalf("set = %+v (%q)", set, set.Unavailable)
	}
}

func TestBaselinesHeuristicCreatedTarget(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	r.Baselines = true
	set := baselinesAround(t, r, "echo hi > made.txt", func() {
		write(t, filepath.Join(dir, "made.txt"), "hi\n")
	})
	if b, ok := byBase(set)["made.txt"]; !ok || b.Existed {
		t.Fatalf("set = %+v (%q)", set, set.Unavailable)
	}
}

func TestBaselinesUnknownCommandOutsideGitIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	r := NewRecorder(dir)
	if r.RepoRoot != "" {
		t.Skip("temp dir is inside a git repository")
	}
	r.Baselines = true
	set := baselinesAround(t, r, "some-tool --do-things", func() {})
	if set.Unavailable == "" {
		t.Fatalf("set = %+v, want an unavailable reason", set)
	}
}

func TestBaselinesNilSnapshot(t *testing.T) {
	var s *Snapshot
	if set := s.Baselines(context.Background()); len(set.Items) != 0 || set.Unavailable != "" {
		t.Fatalf("set = %+v", set)
	}
}

func TestBaselinesCapsTheNumberOfPaths(t *testing.T) {
	dir, r := baselineRepo(t)
	paths := []string{}
	for i := 0; i < MaxBaselinePaths+5; i++ {
		paths = append(paths, filepath.Join(dir, "gen", strings.Repeat("a", 1)+itoa(i)+".txt"))
	}
	set := baselinesAround(t, r, "gen", func() {
		for _, p := range paths {
			write(t, p, "x")
		}
	})
	sort.Slice(set.Items, func(i, j int) bool { return set.Items[i].Path < set.Items[j].Path })
	if len(set.Items) != MaxBaselinePaths || set.Overflow != 5 {
		t.Fatalf("items = %d overflow = %d", len(set.Items), set.Overflow)
	}
}

// gitIn runs git in dir with a hermetic environment.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
