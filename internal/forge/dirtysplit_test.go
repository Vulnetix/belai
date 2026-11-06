package forge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseStatusZ(t *testing.T) {
	// A rename reports its origin as its own field; a name with a space, a
	// newline and a non-ASCII rune arrives raw under -z; a name that is
	// not UTF-8 safe to trim stays exact.
	out := " M a.go\x00R  new name.go\x00old.go\x00?? dir with\nnewline/é.txt\x00A  added.go\x00"
	got, err := parseStatusZ([]byte(out), "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.go", "added.go", "dir with\nnewline/é.txt", "new name.go", "old.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestParseStatusZLeadingSpaceSurvives(t *testing.T) {
	// The first field starts with a significant space; a trimmed read would
	// lose the status column and mangle the first path.
	got, err := parseStatusZ([]byte(" M first.go\x00 D second.go\x00"), "")
	if err != nil || !reflect.DeepEqual(got, []string{"first.go", "second.go"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestParseStatusZPrefixDropsPathsOutsideTheWorkingDirectory(t *testing.T) {
	got, err := parseStatusZ([]byte(" M sub/a.go\x00 M other/b.go\x00?? sub/deep/c.go\x00"), "sub/")
	if err != nil || !reflect.DeepEqual(got, []string{"a.go", "deep/c.go"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestParseStatusZRefusesAnUnboundedTree(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= MaxDirtyPaths; i++ {
		b.WriteString("?? f" + strings.Repeat("x", 3) + string(rune('a'+i%26)) + "/" + itoa(i) + "\x00")
	}
	if _, err := parseStatusZ([]byte(b.String()), ""); err == nil {
		t.Fatal("a tree past the cap must fail closed")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestDirtyPathsFailsClosed(t *testing.T) {
	r := func(_ context.Context, _ string, argv ...string) ([]byte, error) {
		if argv[1] == "rev-parse" {
			return []byte("\n"), nil
		}
		return nil, errors.New("git: boom")
	}
	if _, err := DirtyPaths(context.Background(), r, "."); err == nil {
		t.Fatal("an unreadable status must be an error")
	}
}

func TestSplitDirty(t *testing.T) {
	before := []string{"pre.go", "both.go"}
	now := []string{"pre.go", "both.go", "task.go", "other.go", ".env", "big.bin", "app.log", "lib/x.go"}
	task := []string{"task.go", "both.go", ".env", "gone.go", "app.log", "lib/x.go"}
	s := SplitDirty(before, now, task, SplitOptions{
		Exclude:  []string{"*.log"},
		Withhold: func(p string) bool { return p == ".env" },
	})
	check := func(name string, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	check("Task", s.Task, []string{"lib/x.go", "task.go"})
	check("Mixed", s.Mixed, []string{"both.go"})
	check("Withheld", s.Withheld, []string{".env", "app.log"})
	check("PreExisting", s.PreExisting, []string{"pre.go"})
	check("Unrelated", s.Unrelated, []string{"big.bin", "other.go"})
	// A task path that is not dirty (unchanged, ignored, already committed)
	// is in no list.
	for _, l := range [][]string{s.Task, s.Mixed, s.Withheld, s.PreExisting, s.Unrelated} {
		for _, p := range l {
			if p == "gone.go" {
				t.Fatal("a clean path was listed")
			}
		}
	}
}

func TestSplitDirtyNeverStagesMixedOrGit(t *testing.T) {
	s := SplitDirty([]string{"m.go"}, []string{"m.go", ".git/config", "sub/.git/x"}, []string{"m.go", ".git/config", "sub/.git/x"}, SplitOptions{})
	if len(s.Task) != 0 || !reflect.DeepEqual(s.Mixed, []string{"m.go"}) || len(s.Withheld) != 2 {
		t.Fatalf("split = %+v", s)
	}
}

func TestSplitDirtySizeCap(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "small.txt"), "x")
	writeFile(t, filepath.Join(root, "big.txt"), strings.Repeat("x", 100))
	s := SplitDirty(nil, []string{"small.txt", "big.txt"}, []string{"small.txt", "big.txt"}, SplitOptions{Root: root, MaxFileBytes: 50})
	if !reflect.DeepEqual(s.Task, []string{"small.txt"}) || !reflect.DeepEqual(s.Withheld, []string{"big.txt"}) {
		t.Fatalf("split = %+v", s)
	}
	// A symlink is not sized: its target is not followed.
	if err := os.Symlink(filepath.Join(root, "big.txt"), filepath.Join(root, "link")); err == nil {
		s = SplitDirty(nil, []string{"link"}, []string{"link"}, SplitOptions{Root: root, MaxFileBytes: 50})
		if len(s.Task) != 1 {
			t.Fatalf("a symlink must not be sized through: %+v", s)
		}
	}
}

func TestMatchExclude(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"*.log", "a.log", true},
		{"*.log", "deep/dir/a.log", true},
		{"*.log", "a.logx", false},
		{"secret.txt", "x/secret.txt", true},
		{"build/", "build/out.o", true},
		{"build/", "src/build/out.o", true},
		{"build/", "build", false}, // a file named build is not the directory
		{"docs/gen", "docs/gen/a.md", true},
		{"docs/gen", "docs/other/a.md", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/sub/a.md", false},
		{"[", "a", false}, // a bad pattern matches nothing rather than everything
	}
	for _, tc := range cases {
		if got := MatchExclude(tc.glob, tc.path); got != tc.want {
			t.Errorf("MatchExclude(%q, %q) = %v, want %v", tc.glob, tc.path, got, tc.want)
		}
	}
}

func TestDirtyPathsAndFailedHookOnARealRepo(t *testing.T) {
	dir := newCommitTestRepo(t)
	writeFile(t, filepath.Join(dir, "sub", "keep.txt"), "base\n")
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-q", "-m", "base")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho no >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "sub", "keep.txt"), "changed\n")
	writeFile(t, filepath.Join(dir, "sub", "new name.txt"), "new\n")
	writeFile(t, filepath.Join(dir, "outside.txt"), "x\n")
	sub := filepath.Join(dir, "sub")

	got, err := DirtyPaths(context.Background(), ExecRunner, sub)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"keep.txt", "new name.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths from a subdirectory = %q, want %q", got, want)
	}
	head := HeadCommit(context.Background(), ExecRunner, sub)

	_, err = CommitPaths(context.Background(), ExecRunner, sub, got, "feat: add things")
	if err == nil {
		t.Fatal("the pre-commit hook should have refused")
	}
	if HeadCommit(context.Background(), ExecRunner, sub) != head {
		t.Fatal("HEAD moved on a refused commit")
	}
	if staged := gitOut(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("a refused commit left paths staged: %q", staged)
	}
	if body, _ := os.ReadFile(filepath.Join(sub, "keep.txt")); string(body) != "changed\n" {
		t.Fatalf("the working tree changed: %q", body)
	}
	if st := gitOut(t, dir, "status", "--porcelain"); !strings.Contains(st, "keep.txt") || !strings.Contains(st, "outside.txt") {
		t.Fatalf("status = %q", st)
	}
}

func TestDirtyPathsSeesARename(t *testing.T) {
	dir := newCommitTestRepo(t)
	gitOut(t, dir, "mv", "tracked.go", "moved file.go")
	got, err := DirtyPaths(context.Background(), ExecRunner, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"moved file.go", "tracked.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a rename must list both names: %q", got)
	}
}
