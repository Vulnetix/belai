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
