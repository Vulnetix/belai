package rc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/trustgate"
)

// gitRepo makes dir a minimal repository root.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A directory inside a repository whose root is not trusted is never offered:
// `belai agent start` checks trust at the root, so offering it would only fail
// after the website picked it.
func TestCollectSkipsDirsInUntrustedRepos(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	base, _ := Normalize(t.TempDir())
	repo := filepath.Join(base, "repo")
	sub := filepath.Join(repo, "sub")
	plain := filepath.Join(base, "plain")
	for _, d := range []string{sub, plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitRepo(t, repo)

	offered, skipped := Collect([]string{sub, repo, plain}, "")
	if len(offered) != 2 || offered[0].Path != repo || offered[1].Path != plain {
		t.Fatalf("offered = %+v", offered)
	}
	if len(skipped) != 1 || skipped[0].Dir.Path != sub || skipped[0].Root != repo {
		t.Fatalf("skipped = %+v", skipped)
	}

	// Trusting the repository root makes the subdirectory startable.
	if err := trustgate.Grant(repo, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := Startable(sub); !ok {
		t.Fatal("subdirectory of a trusted repository not startable")
	}
}

// A stray .git without a HEAD above an offered directory does not make its
// parent the repository to trust.
func TestStartableIgnoresStrayParentGit(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	base, _ := Normalize(t.TempDir())
	if err := os.MkdirAll(filepath.Join(base, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if root, ok := Startable(proj); !ok || root != proj {
		t.Fatalf("Startable = %q, %v", root, ok)
	}
}

// The directory rc was started in leads the trusted offers, so a start the
// website does not place lands where the host user ran rc.
func TestHereFirst(t *testing.T) {
	dirs := []Dir{{Path: "/b"}, {Path: "/c"}, {Path: "/a"}}
	got := hereFirst(dirs, "/c")
	if got[0].Path != "/c" || got[1].Path != "/a" || got[2].Path != "/b" {
		t.Fatalf("order = %+v", got)
	}
	got = hereFirst(got, "/elsewhere")
	if got[0].Path != "/a" || got[1].Path != "/b" || got[2].Path != "/c" {
		t.Fatalf("order without here = %+v", got)
	}
}

func TestAllowedExtra(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	var offered []Dir
	for _, p := range []string{a, b} {
		n, err := Normalize(p)
		if err != nil {
			t.Fatal(err)
		}
		offered = append(offered, Dir{Path: n})
	}
	pa, _ := Normalize(a)
	pb, _ := Normalize(b)

	got, ok := AllowedExtra(offered, pa, []string{a, b, b})
	if !ok || len(got) != 1 || got[0] != pb {
		t.Fatalf("primary and repeats should drop: got %v ok=%v", got, ok)
	}
	if _, ok := AllowedExtra(offered, pa, []string{b, c}); ok {
		t.Fatal("a directory the host does not offer was allowed")
	}
	if got, ok := AllowedExtra(offered, pa, nil); !ok || len(got) != 0 {
		t.Fatalf("no extras: got %v ok=%v", got, ok)
	}
	many := make([]string, MaxExtraDirs+1)
	if _, ok := AllowedExtra(offered, pa, many); ok {
		t.Fatal("too many directories allowed")
	}
}

func TestChildArgsAddDir(t *testing.T) {
	args := childArgs(Child{Dispatch: "d", SessionID: "s", Dirs: []string{"/x", "/y"}})
	var got []string
	for i, a := range args {
		if a == "-add-dir" && i+1 < len(args) {
			got = append(got, args[i+1])
		}
	}
	if len(got) != 2 || got[0] != "/x" || got[1] != "/y" {
		t.Fatalf("args = %v", args)
	}
}
