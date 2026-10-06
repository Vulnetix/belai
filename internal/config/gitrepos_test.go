package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitRepoHelpers(t *testing.T) {
	r := GitRepo{Name: "app"}
	if !r.IsEnabled() || r.Subdir() != "app" {
		t.Errorf("defaults: enabled %v dir %q", r.IsEnabled(), r.Subdir())
	}
	f := false
	r.Enabled, r.Dir = &f, "team/app"
	if r.IsEnabled() || r.Subdir() != "team/app" {
		t.Errorf("set: enabled %v dir %q", r.IsEnabled(), r.Subdir())
	}
	s := Settings{GitRepos: []GitRepo{{Name: "a"}, {Name: "b"}}}
	if got, ok := s.GitRepoNamed("b"); !ok || got.Name != "b" {
		t.Errorf("GitRepoNamed(b) = %+v %v", got, ok)
	}
	if _, ok := s.GitRepoNamed("nope"); ok {
		t.Error("GitRepoNamed found a repo that is not there")
	}
	t.Setenv("BELAI_HOME", "/state")
	if d, err := ReposDir(); err != nil || d != filepath.Join("/state", "repos") {
		t.Errorf("ReposDir = %q %v", d, err)
	}
}

// The list of repositories to keep is the user's own: the project layer is
// dropped, with a note, and never reaches the effective settings.
func TestProjectReposAreDropped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"repos":[{"name":"mine","url":"https://github.com/me/mine.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".vulnetix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectSettingsPath(proj), []byte(`{"repos":[{"name":"theirs","url":"https://evil.example/x.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(proj, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Settings.GitRepos) != 1 || eff.Settings.GitRepos[0].Name != "mine" {
		t.Fatalf("effective repos = %+v", eff.Settings.GitRepos)
	}
	if eff.Origin["repos"] != SourceGlobal {
		t.Errorf("origin = %q", eff.Origin["repos"])
	}
	noted := false
	for _, n := range eff.Notes {
		if n == "project repos ignored (the list of repositories to keep is global; see belai repo)" {
			noted = true
		}
	}
	if !noted {
		t.Errorf("no note about the dropped project repos: %v", eff.Notes)
	}
	// Settings.Override, the merge the TUI uses, drops it too.
	merged, err := LoadMerged(proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.GitRepos) != 1 || merged.GitRepos[0].Name != "mine" {
		t.Fatalf("merged repos = %+v", merged.GitRepos)
	}
}
