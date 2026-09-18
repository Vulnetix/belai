package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/repoindex"
)

// repoTestCaps is the capability set that offers every repo tool: RepoFiles
// shells out to git and RepoRead to cat, and both gate on their binary.
func repoTestCaps() Capabilities {
	return Capabilities{local: map[string]bool{"Git": true, "Cat": true}}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = dir
	cmd.Run()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "file.txt")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-q", "-m", "initial")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "remote", "add", "origin", "git@github.com:Vulnetix/signet.git")
	cmd.Dir = dir
	cmd.Run()
}

func TestRepoToolsAbsentWhenIndexEmpty(t *testing.T) {
	reg := DefaultWithCaps(t.TempDir(), true, Capabilities{}, repoindex.Index{})
	for _, name := range []string{"Repos", "RepoFiles", "RepoRead"} {
		if _, ok := reg.Find(name); ok {
			t.Errorf("%q should not be registered with an empty index", name)
		}
	}
}

func TestRepoToolsPresentWhenIndexNonEmpty(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Index{}
	// Use reflection-free access through Scan.
	ix = repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	if ix.Empty() {
		t.Fatal("expected index to find the repo")
	}
	reg := DefaultWithCaps(t.TempDir(), true, repoTestCaps(), ix)
	for _, name := range []string{"Repos", "RepoFiles", "RepoRead"} {
		if _, ok := reg.Find(name); !ok {
			t.Errorf("%q should be registered with a non-empty index", name)
		}
	}
}

// The repo tools that shell out fail closed when their binary was not
// detected; the in-process listing is unaffected, because it needs no
// binary at all.
func TestRepoToolsGatedOnBinaries(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))

	cases := []struct {
		caps    Capabilities
		wantGit bool // RepoFiles
		wantCat bool // RepoRead
	}{
		{Capabilities{}, false, false},
		{Capabilities{local: map[string]bool{"Git": true}}, true, false},
		{Capabilities{local: map[string]bool{"Cat": true}}, false, true},
		{repoTestCaps(), true, true},
	}
	for _, tc := range cases {
		reg := DefaultWithCaps(t.TempDir(), true, tc.caps, ix)
		if _, ok := reg.Find("Repos"); !ok {
			t.Fatal("Repos must be offered regardless of capabilities")
		}
		if _, ok := reg.Find("RepoFiles"); ok != tc.wantGit {
			t.Errorf("RepoFiles offered = %v, want %v (caps %v)", ok, tc.wantGit, tc.caps.LocalNames())
		}
		if _, ok := reg.Find("RepoRead"); ok != tc.wantCat {
			t.Errorf("RepoRead offered = %v, want %v (caps %v)", ok, tc.wantCat, tc.caps.LocalNames())
		}
	}
}

func TestRepoReadConfinesToCheckout(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	reg := DefaultWithCaps(t.TempDir(), true, repoTestCaps(), ix)
	tool, ok := reg.Find("RepoRead")
	if !ok {
		t.Fatal("RepoRead not registered")
	}
	res, err := tool.Execute(context.Background(), map[string]any{"repo": "Vulnetix/signet", "path": "file.txt"})
	if err != nil {
		t.Fatalf("RepoRead: %v", err)
	}
	if res.Kind != KindRead {
		t.Fatalf("RepoRead kind = %q, want %q", res.Kind, KindRead)
	}
	if res.Content != "hello" {
		t.Fatalf("RepoRead content = %q, want hello", res.Content)
	}
}

func TestRepoReadRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	other := filepath.Join(dir, "other.txt")
	if err := os.MkdirAll(repo, 0o755); err != nil || os.WriteFile(other, []byte("secret"), 0o644) != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other-dir"))
	reg := DefaultWithCaps(t.TempDir(), true, repoTestCaps(), ix)
	tool, _ := reg.Find("RepoRead")
	_, err := tool.Execute(context.Background(), map[string]any{"repo": "Vulnetix/signet", "path": "../other.txt"})
	if err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestRepoFilesListsFiles(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	reg := DefaultWithCaps(t.TempDir(), true, repoTestCaps(), ix)
	tool, ok := reg.Find("RepoFiles")
	if !ok {
		t.Fatal("RepoFiles not registered")
	}
	res, err := tool.Execute(context.Background(), map[string]any{"repo": "Vulnetix/signet"})
	if err != nil {
		t.Fatalf("RepoFiles: %v", err)
	}
	if res.Kind != KindNative {
		t.Fatalf("RepoFiles kind = %q, want %q", res.Kind, KindNative)
	}
	if !slices.Contains(strings.Fields(res.Content), "file.txt") {
		t.Fatalf("RepoFiles missing file.txt in %q", res.Content)
	}
}

func TestRepoFilesMissListsAvailable(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	reg := DefaultWithCaps(t.TempDir(), true, repoTestCaps(), ix)
	tool, _ := reg.Find("RepoFiles")
	_, err := tool.Execute(context.Background(), map[string]any{"repo": "Unknown/Repo"})
	if err == nil {
		t.Fatal("expected miss error")
	}
	if !slices.Contains([]string{"Vulnetix/signet"}, "Vulnetix/signet") {
		// sanity: available repo name is what we expect
	}
	if !strings.Contains(err.Error(), "Vulnetix/signet") {
		t.Fatalf("miss error should list available repo: %v", err)
	}
}

func TestCatalogueNamesIncludesRepoTools(t *testing.T) {
	names := CatalogueNames()
	for _, want := range []string{"Repos", "RepoFiles", "RepoRead"} {
		if !slices.Contains(names, want) {
			t.Errorf("CatalogueNames missing %q", want)
		}
	}
}

// The Repos listing runs in-process: no binary, no capabilities, no
// subprocess. This is the case that used to exec.Command("repos") and fail
// with "executable file not found in $PATH".
func TestReposExecutesInProcess(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	reg := DefaultWithCaps(t.TempDir(), true, Capabilities{}, ix)
	tool, ok := reg.Find("Repos")
	if !ok {
		t.Fatal("Repos not registered")
	}
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Repos: %v", err)
	}
	if res.Kind != KindNative {
		t.Fatalf("Repos kind = %q, want %q", res.Kind, KindNative)
	}
	if !strings.Contains(res.Content, "Vulnetix/signet") || !strings.Contains(res.Content, repo) {
		t.Fatalf("Repos listing missing the indexed repo:\n%s", res.Content)
	}
	if got := tool.Subject(map[string]any{"owner": "Vulnetix"}); got != "Vulnetix" {
		t.Fatalf("Repos subject = %q, want Vulnetix", got)
	}
}

func TestReposOwnerFilter(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "signet")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	initRepo(t, repo)
	ix := repoindex.Scan(context.Background(), filepath.Join(dir, "other"))
	reg := DefaultWithCaps(t.TempDir(), true, Capabilities{}, ix)
	tool, _ := reg.Find("Repos")

	res, err := tool.Execute(context.Background(), map[string]any{"owner": "Vulnetix"})
	if err != nil {
		t.Fatalf("Repos(owner): %v", err)
	}
	if !strings.Contains(res.Content, "Vulnetix/signet") {
		t.Fatalf("owner filter dropped the matching repo:\n%s", res.Content)
	}

	// A filter that matches nothing must say so, not return an empty answer
	// that reads as "no repositories at all".
	res, err = tool.Execute(context.Background(), map[string]any{"owner": "Nobody"})
	if err != nil {
		t.Fatalf("Repos(no match): %v", err)
	}
	want := `no repositories owned by "Nobody" in the local index`
	if res.Content != want {
		t.Fatalf("no-match listing = %q, want %q", res.Content, want)
	}
}

func TestReposAbsentWhenIndexEmpty(t *testing.T) {
	reg := DefaultWithCaps(t.TempDir(), true, Capabilities{}, repoindex.Index{})
	if _, ok := reg.Find("Repos"); ok {
		t.Fatal("Repos must not be offered for an empty index")
	}
}
