package repoindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a minimal git repository in dir with the given origin.
func initRepo(t *testing.T, dir, origin string) {
	t.Helper()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.email", "test@example.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "remote", "add", "origin", origin)
	run(t, dir, "git", "commit", "-q", "--allow-empty", "-m", "initial")
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v in %s: %v\n%s", name, args, dir, err, out)
	}
}

func TestScanFindsDepthOneRepo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "belai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir, "git@github.com:Vulnetix/belai.git")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	e, ok := ix.Lookup("Vulnetix/belai")
	if !ok {
		t.Fatalf("Lookup failed, entries=%v", ix.Entries())
	}
	if e.Name != "belai" || e.Owner != "Vulnetix" || e.Host != "github.com" {
		t.Fatalf("Entry = %+v", e)
	}
}

func TestScanFindsDepthTwoRepo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Vulnetix", "belai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir, "https://github.com/Vulnetix/belai.git")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	if _, ok := ix.Lookup("Vulnetix/belai"); !ok {
		t.Fatalf("depth-2 lookup failed, entries=%v", ix.Entries())
	}
}

func TestScanDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkDir := filepath.Join(root, "link")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, realDir, "git@github.com:Vulnetix/real.git")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	if len(ix.Entries()) != 1 {
		t.Fatalf("expected one entry, got %v", ix.Entries())
	}
	if !strings.Contains(ix.Entries()[0].Path, "real") {
		t.Fatalf("symlink followed: %v", ix.Entries())
	}
}

func TestScanListsUnparseableRemote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "weird")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir, "not-a-url")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	ents := ix.Entries()
	if len(ents) != 1 {
		t.Fatalf("expected one entry, got %v", ents)
	}
	if ents[0].Owner != "" || ents[0].Name != "" || ents[0].Host != "" {
		t.Fatalf("unparseable remote produced owner/name/host: %+v", ents[0])
	}
}

func TestLookupBareNameRequiresUniqueness(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "OrgA", "belai")
	b := filepath.Join(root, "OrgB", "belai")
	if err := os.MkdirAll(a, 0o755); err != nil || os.MkdirAll(b, 0o755) != nil {
		t.Fatal(err)
	}
	initRepo(t, a, "git@github.com:OrgA/belai.git")
	initRepo(t, b, "git@github.com:OrgB/belai.git")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	if _, ok := ix.Lookup("belai"); ok {
		t.Fatal("ambiguous bare name should not match")
	}
	if _, ok := ix.Lookup("OrgA/belai"); !ok {
		t.Fatal("owner/repo lookup should succeed")
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "belai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, dir, "git@github.com:Vulnetix/Belai.git")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	if _, ok := ix.Lookup("vulnetix/belai"); !ok {
		t.Fatal("case-insensitive lookup failed")
	}
}

func TestScanCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxEntries+5; i++ {
		dir := filepath.Join(root, fmtRepo(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		initRepo(t, dir, "git@github.com:Org/repo.git")
	}

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	if len(ix.Entries()) > maxEntries {
		t.Fatalf("entries = %d, want <= %d", len(ix.Entries()), maxEntries)
	}
}

func fmtRepo(i int) string {
	return filepath.Join("orgs", "repo"+string(rune('a'+i%26))+string(rune('0'+i/26)))
}

func TestScanFindsWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	mainRepo := filepath.Join(root, "main")
	worktree := filepath.Join(root, "work")
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, mainRepo, "git@github.com:Vulnetix/belai.git")
	if _, err := os.Create(filepath.Join(mainRepo, "file.txt")); err != nil {
		t.Fatal(err)
	}
	run(t, mainRepo, "git", "add", "file.txt")
	run(t, mainRepo, "git", "commit", "-q", "-m", "file")
	run(t, mainRepo, "git", "branch", "feature")
	run(t, mainRepo, "git", "worktree", "add", "-q", worktree, "feature")

	ix := Scan(context.Background(), filepath.Join(root, "other"))
	found := false
	for _, e := range ix.Entries() {
		if strings.Contains(e.Path, "work") {
			found = true
			if e.Owner != "Vulnetix" || e.Name != "belai" {
				t.Fatalf("worktree entry malformed: %+v", e)
			}
			if e.Branch != "feature" {
				t.Fatalf("worktree branch = %q, want feature", e.Branch)
			}
		}
	}
	if !found {
		t.Fatalf("worktree not indexed: %v", ix.Entries())
	}
}

func TestParseRemoteShapes(t *testing.T) {
	cases := []struct {
		raw, host, owner, name string
	}{
		{"git@github.com:Vulnetix/belai.git", "github.com", "Vulnetix", "belai"},
		{"ssh://git@github.com/Vulnetix/belai.git", "github.com", "Vulnetix", "belai"},
		{"https://github.com/Vulnetix/belai.git", "github.com", "Vulnetix", "belai"},
		{"https://github.com/Vulnetix/belai", "github.com", "Vulnetix", "belai"},
		{"github.com:Vulnetix/belai", "github.com", "Vulnetix", "belai"},
		{"git@gitlab.com:org/repo.git", "gitlab.com", "org", "repo"},
	}
	for _, tc := range cases {
		host, owner, name, ok := parseRemote(tc.raw)
		if !ok {
			t.Errorf("parseRemote(%q) failed", tc.raw)
			continue
		}
		if host != tc.host || owner != tc.owner || name != tc.name {
			t.Errorf("parseRemote(%q) = %q/%q/%q, want %q/%q/%q", tc.raw, host, owner, name, tc.host, tc.owner, tc.name)
		}
	}
}

func TestParseRemoteRejectsUnparseable(t *testing.T) {
	if _, _, _, ok := parseRemote("not-a-url"); ok {
		t.Fatal("expected unparseable remote to fail")
	}
}

func TestEntryString(t *testing.T) {
	cases := []struct {
		e    Entry
		want string
	}{
		{Entry{Path: "/p"}, "/p"},
		{Entry{Path: "/p", Owner: "O", Name: "N", Host: "h"}, "h/O/N /p"},
		{Entry{Path: "/p", Owner: "O", Name: "N", Host: "h", Branch: "main"}, "h/O/N /p (branch main)"},
	}
	for _, tc := range cases {
		if got := tc.e.String(); got != tc.want {
			t.Errorf("String(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestIndexOwnerAndEmpty(t *testing.T) {
	ix := Index{entries: []Entry{
		{Owner: "A", Name: "r1"},
		{Owner: "B", Name: "r2"},
		{Owner: "a", Name: "r3"},
	}}
	if ix.Empty() {
		t.Fatal("non-empty index reported empty")
	}
	if got := ix.Owner("a"); len(got) != 2 {
		t.Fatalf("Owner(a) = %d entries, want 2 (case-insensitive)", len(got))
	}
	if got := (Index{}).Owner("a"); len(got) != 0 {
		t.Fatalf("empty index Owner = %v, want empty", got)
	}
	if !(Index{}).Empty() {
		t.Fatal("zero index should be empty")
	}
}
