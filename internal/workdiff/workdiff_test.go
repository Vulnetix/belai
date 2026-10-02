package workdiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q")
	write(t, dir, "a.txt", "one\ntwo\nthree\n")
	write(t, dir, "b.txt", "keep\n")
	write(t, dir, "secret.env", "TOKEN=old\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func find(s Snapshot, p string) *File {
	for i := range s.Files {
		if s.Files[i].Path == p {
			return &s.Files[i]
		}
	}
	return nil
}

func TestCollectStagedUnstagedUntracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, "a.txt", "one\nTWO\nthree\n")
	write(t, dir, "b.txt", "keep\nmore\n")
	run(t, dir, "add", "b.txt")
	write(t, dir, "sub/new.txt", "fresh\n")

	s := Collect(context.Background(), dir, nil)
	if s.Unavailable != "" {
		t.Fatal(s.Unavailable)
	}
	if s.Staged != 1 || s.Unstaged != 1 || s.Untracked != 1 {
		t.Fatalf("counts staged=%d unstaged=%d untracked=%d", s.Staged, s.Unstaged, s.Untracked)
	}
	if len(s.Files) != 3 {
		t.Fatalf("files = %d", len(s.Files))
	}
	a := find(s, "a.txt")
	if a == nil || !a.Unstaged || a.Staged || a.Added != 1 || a.Removed != 1 {
		t.Fatalf("a.txt = %+v", a)
	}
	n := find(s, "sub/new.txt")
	if n == nil || !n.Untracked || !n.Change.Created || n.Added != 1 {
		t.Fatalf("new.txt = %+v", n)
	}
	if s.Added != 3 || s.Removed != 1 {
		t.Fatalf("totals +%d -%d", s.Added, s.Removed)
	}
}

func TestCollectDeletedAndStagedNew(t *testing.T) {
	dir := repo(t)
	if err := os.Remove(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "c.txt", "c\n")
	run(t, dir, "add", "c.txt")
	s := Collect(context.Background(), dir, nil)
	if b := find(s, "b.txt"); b == nil || !b.Change.Deleted || b.Removed != 1 {
		t.Fatalf("b.txt = %+v", b)
	}
	if c := find(s, "c.txt"); c == nil || !c.Staged || !c.Change.Created {
		t.Fatalf("c.txt = %+v", c)
	}
}

func TestCollectCleanTree(t *testing.T) {
	s := Collect(context.Background(), repo(t), nil)
	if s.Unavailable != "" || len(s.Files) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestCollectNotARepository(t *testing.T) {
	s := Collect(context.Background(), t.TempDir(), nil)
	if s.Unavailable == "" {
		t.Fatal("expected unavailable")
	}
}

func TestHiddenPathReadsNoContent(t *testing.T) {
	dir := repo(t)
	write(t, dir, "secret.env", "TOKEN=new-value\n")
	s := Collect(context.Background(), dir, func(rel string) bool { return rel == "secret.env" })
	f := find(s, "secret.env")
	if f == nil || !f.Hidden || f.Added != 0 || f.Change.New != "" || f.Change.Old != "" {
		t.Fatalf("secret.env = %+v", f)
	}
	if strings.Contains(f.Note, "new-value") {
		t.Fatal("note leaks content")
	}
}

func TestSymlinkIsNotFollowed(t *testing.T) {
	dir := repo(t)
	outside := filepath.Join(t.TempDir(), "target")
	write(t, filepath.Dir(outside), "target", "outside content\n")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	s := Collect(context.Background(), dir, nil)
	f := find(s, "link")
	if f == nil || f.Note != "symbolic link" || f.Change.New != "" {
		t.Fatalf("link = %+v", f)
	}
}

func TestBinaryAndEscapeSequences(t *testing.T) {
	dir := repo(t)
	write(t, dir, "bin.dat", "a\x00b")
	write(t, dir, "esc.txt", "hello \x1b]0;pwned\x07 world\n")
	s := Collect(context.Background(), dir, nil)
	if f := find(s, "bin.dat"); f == nil || !f.Change.Binary {
		t.Fatalf("bin.dat = %+v", f)
	}
	f := find(s, "esc.txt")
	if f == nil || strings.ContainsRune(f.Change.New, 0x1b) || strings.ContainsRune(f.Change.New, 7) {
		t.Fatalf("esc.txt = %+v", f)
	}
}

func TestLargeFileIsNotDiffed(t *testing.T) {
	dir := repo(t)
	write(t, dir, "big.txt", strings.Repeat("x\n", MaxBytes))
	f := find(Collect(context.Background(), dir, nil), "big.txt")
	if f == nil || f.Note != "too large to diff" || f.Change.New != "" {
		t.Fatalf("big.txt = %+v", f)
	}
}

func TestFreshRepositoryWithoutHead(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	run(t, dir, "init", "-q")
	write(t, dir, "x.txt", "x\n")
	s := Collect(context.Background(), dir, nil)
	if f := find(s, "x.txt"); f == nil || !f.Untracked || f.Added != 1 {
		t.Fatalf("%+v", s.Files)
	}
}

func TestRepositoryHookAndFsmonitorDoNotRun(t *testing.T) {
	dir := repo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "fsm.sh")
	write(t, filepath.Dir(script), "fsm.sh", "#!/bin/sh\ntouch "+marker+"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "config", "core.fsmonitor", script)
	write(t, dir, "a.txt", "changed\n")
	Collect(context.Background(), dir, nil)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository fsmonitor ran")
	}
}

func TestParseStatus(t *testing.T) {
	got := parseStatus([]byte("UU c.txt\x00!! i\x00?? u\x00 M m\x00"))
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if !got[0].Conflict || got[0].Staged || got[0].Unstaged {
		t.Fatalf("conflict = %+v", got[0])
	}
	if !got[1].Untracked || !got[2].Unstaged {
		t.Fatalf("%+v", got)
	}
}
