package libstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

const repoJSON = `{"name":"app","url":"https://github.com/acme/app.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"},{"kind":"tag","name":"v1"}],"dir":"acme/app","depth":50,"submodules":true,"enabled":true}`

func settingsRepos(t *testing.T) []config.GitRepo {
	t.Helper()
	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	return s.GitRepos
}

func TestRepoInstallWritesTheGlobalSettingsAndExportsTheSameBytes(t *testing.T) {
	h := home(t)
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(`{"model":"gpt-5","vulnetix_custom":{"keep":"me"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Install(libitem.Repo, []byte(repoJSON), InstallOptions{Name: "app"})
	if err != nil || res.Replaced || !strings.Contains(res.Where, "settings.json") {
		t.Fatalf("install: %+v %v", res, err)
	}
	rs := settingsRepos(t)
	if len(rs) != 1 || rs[0].Name != "app" || rs[0].Dir != "acme/app" || rs[0].Depth == nil || *rs[0].Depth != 50 || len(rs[0].Refs) != 2 {
		t.Fatalf("settings repos = %+v", rs)
	}
	// What else the file held is untouched.
	b, _ := os.ReadFile(filepath.Join(h, "settings.json"))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || string(raw["model"]) != `"gpt-5"` || !strings.Contains(string(raw["vulnetix_custom"]), "keep") {
		t.Fatalf("settings.json = %s", b)
	}
	want, _ := libitem.Validate(libitem.Repo, []byte(repoJSON))
	got, err := Get(libitem.Repo, "app")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
}

func TestRepoInstallNeedsOverwriteToReplaceAndKeepsTheOthers(t *testing.T) {
	home(t)
	other := strings.Replace(strings.Replace(repoJSON, `"name":"app"`, `"name":"other"`, 1), `"dir":"acme/app"`, `"dir":"acme/other"`, 1)
	for _, d := range []string{repoJSON, other} {
		if _, err := Install(libitem.Repo, []byte(d), InstallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	changed := strings.Replace(repoJSON, `"depth":50`, `"depth":1`, 1)
	if _, err := Install(libitem.Repo, []byte(changed), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if rs := settingsRepos(t); *rs[0].Depth != 50 {
		t.Fatal("a refused install changed the entry")
	}
	res, err := Install(libitem.Repo, []byte(changed), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	rs := settingsRepos(t)
	if len(rs) != 2 || rs[0].Name != "app" || *rs[0].Depth != 1 || rs[1].Name != "other" {
		t.Fatalf("after overwrite: %+v", rs)
	}
}

func TestRepoInstallRefusals(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Repo, []byte(repoJSON), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"a directory another repository uses", strings.Replace(repoJSON, `"name":"app"`, `"name":"twin"`, 1), `already uses the directory "acme/app"`},
		{"a token in the url", strings.Replace(repoJSON, `https://github.com`, `https://ghp_x@github.com`, 1), "must not carry credentials"},
		{"a private repo with no installation", strings.Replace(strings.Replace(repoJSON, `"public"`, `"private"`, 1), `"none"`, `"github_app"`, 1), "installation_id is required"},
		{"a missing key", strings.Replace(repoJSON, `"enabled":true`, `"x":1`, 1), "unknown field"},
		{"a ref that looks like an option", strings.Replace(repoJSON, `"main"`, `"-main"`, 1), "not a valid git branch name"},
		{"a dir that climbs", strings.Replace(repoJSON, `"acme/app"`, `"../x"`, 1), "empty, . or .. segment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Install(libitem.Repo, []byte(c.doc), InstallOptions{Overwrite: true})
			if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
	if rs := settingsRepos(t); len(rs) != 1 {
		t.Fatalf("a refused install changed the settings: %+v", rs)
	}
}

func TestRepoHandWrittenEntryExportsEveryRequiredKey(t *testing.T) {
	h := home(t)
	// An entry that leaves the defaults out, as a person would write it.
	hand := `{"repos":[{"name":"mini","url":"git@github.com:acme/mini.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}]},
	  {"name":"broken","url":"http://insecure.example/x.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}]},
	  {"name":"mini","url":"https://github.com/acme/dup.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}]}]}`
	if err := os.WriteFile(filepath.Join(h, "settings.json"), []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	items, skipped, err := List(libitem.Repo)
	if err != nil || len(items) != 1 || len(skipped) != 2 {
		t.Fatalf("items %+v skipped %+v %v", items, skipped, err)
	}
	r, err := libitem.ParseRepo(items[0].Doc)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir != "mini" || r.Depth == nil || *r.Depth != 0 || r.Submodules == nil || *r.Submodules || !r.IsEnabled() {
		t.Fatalf("the defaults are not spelled out: %s", items[0].Doc)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Name] = s.Reason
	}
	if !strings.Contains(reasons["broken"], "must be https://") {
		t.Errorf("skipped = %+v", skipped)
	}
}

// The project layer's repos is never listed, and never written.
func TestRepoProjectSettingsAreNeverRead(t *testing.T) {
	home(t)
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".vulnetix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".vulnetix", "settings.json"), []byte(`{"repos":[`+repoJSON+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, _, err := List(libitem.Repo)
	if err != nil || len(items) != 0 {
		t.Fatalf("items = %+v %v", items, err)
	}
	eff, err := config.Resolve(proj, func(string) string { return "" }, config.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Settings.GitRepos) != 0 {
		t.Fatalf("the project layer's repos reached the effective settings: %+v", eff.Settings.GitRepos)
	}
}
