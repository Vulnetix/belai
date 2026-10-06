package rc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
)

func gitDirFixture(t *testing.T, head, origin, originHead string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, ".git", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("HEAD", head+"\n")
	write("config", "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = "+origin+"\n")
	if originHead != "" {
		write("refs/remotes/origin/HEAD", originHead+"\n")
	}
	return root
}

func TestDirGitCarriesIdentifierFactsAndNoCredential(t *testing.T) {
	root := gitDirFixture(t, "ref: refs/heads/feature/login", "https://alice:ghp_secretToken@github.com/Acme/api.git", "ref: refs/remotes/origin/main")
	got := dirGit(sessionsync.RCDir{Path: root, Name: "api", Source: "trusted"})
	if got.Remote != "Acme/api" || got.Host != "github.com" || got.Provider != "github" || got.Branch != "feature/login" || got.DefaultBranch != "main" {
		t.Fatalf("facts = %+v", got)
	}
	b, _ := json.Marshal(got)
	for _, leak := range []string{"alice", "ghp_secretToken", "https://"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("the advertisement carries %q: %s", leak, b)
		}
	}
}

func TestDirGitLeavesOutWhatIsNotAForgeCheckout(t *testing.T) {
	plain := dirGit(sessionsync.RCDir{Path: t.TempDir(), Name: "notes"})
	if plain.Remote != "" || plain.Branch != "" || plain.Host != "" {
		t.Fatalf("a plain directory got facts: %+v", plain)
	}
	// A local-path origin and an odd branch name keep nothing.
	root := gitDirFixture(t, "ref: refs/heads/has space", "/srv/git/repo.git", "")
	got := dirGit(sessionsync.RCDir{Path: root})
	if got.Remote != "" || got.Host != "" || got.Branch != "" || got.DefaultBranch != "" {
		t.Fatalf("facts = %+v", got)
	}
	// The scp form parses, and a detached HEAD has no branch.
	root = gitDirFixture(t, "0123456789abcdef0123456789abcdef01234567", "git@gitlab.com:group/sub/proj.git", "")
	got = dirGit(sessionsync.RCDir{Path: root})
	if got.Remote != "group/sub/proj" || got.Host != "gitlab.com" || got.Provider != "gitlab" || got.Branch != "" {
		t.Fatalf("scp form: %+v", got)
	}
}
