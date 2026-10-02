package libitem

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// pub is a valid public repo document with the given fields replaced.
func repoDoc(over map[string]string) []byte {
	f := map[string]string{
		"name": `"app"`, "url": `"https://github.com/acme/app.git"`, "visibility": `"public"`, "auth": `"none"`,
		"refs": `[{"kind":"branch","name":"main"}]`, "dir": `"app"`, "depth": `0`, "submodules": `false`, "enabled": `true`,
	}
	for k, v := range over {
		if v == "-" {
			delete(f, k)
			continue
		}
		f[k] = v
	}
	var parts []string
	for k, v := range f {
		parts = append(parts, fmt.Sprintf("%q:%s", k, v))
	}
	return []byte("{" + strings.Join(parts, ",") + "}")
}

func TestValidateRepo(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name string
		over map[string]string
		err  string
	}{
		{"a public repo", nil, ""},
		{"a private repo", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `4242`}, ""},

		// Required keys.
		{"no name", map[string]string{"name": "-"}, "name is required"},
		{"no url", map[string]string{"url": "-"}, "repo.url is required"},
		{"no visibility", map[string]string{"visibility": "-"}, "repo.visibility is required"},
		{"no auth", map[string]string{"auth": "-"}, "repo.auth is required"},
		{"no refs", map[string]string{"refs": "-"}, "repo.refs needs at least one ref"},
		{"no dir", map[string]string{"dir": "-"}, "repo.dir is required"},
		{"no depth", map[string]string{"depth": "-"}, "repo.depth is required"},
		{"no submodules", map[string]string{"submodules": "-"}, "repo.submodules is required"},
		{"no enabled", map[string]string{"enabled": "-"}, "repo.enabled is required"},
		{"an unknown key", map[string]string{"token": `"x"`}, `unknown field "token"`},
		{"a key in the wrong case", map[string]string{"URL": `"x"`}, `unknown field "URL"`},
		{"null in place of a string", map[string]string{"dir": `null`}, "repo.dir must be a string"},

		// Names.
		{"an uppercase name", map[string]string{"name": `"App"`}, "is not valid"},
		{"a name at the limit", map[string]string{"name": `"` + long(64) + `"`}, ""},
		{"a name over the limit", map[string]string{"name": `"` + long(65) + `"`}, "is not valid"},

		// URLs.
		{"an https url without .git", map[string]string{"url": `"https://github.com/acme/app"`}, ""},
		{"an https url with a port", map[string]string{"url": `"https://git.example.com:8443/acme/app.git"`}, ""},
		{"an https url with nested path", map[string]string{"url": `"https://gitlab.com/group/sub/app.git"`}, ""},
		{"an scp url", map[string]string{"url": `"git@github.com:acme/app.git"`}, ""},
		{"an scp url without .git", map[string]string{"url": `"git@github.com:acme/app"`}, "is not git@host:path.git"},
		{"an scp url for another user", map[string]string{"url": `"deploy@github.com:acme/app.git"`}, "not a valid URL"},
		{"http", map[string]string{"url": `"http://github.com/acme/app.git"`}, "must be https://"},
		{"ssh://", map[string]string{"url": `"ssh://git@github.com/acme/app.git"`}, "must be https://"},
		{"git://", map[string]string{"url": `"git://github.com/acme/app.git"`}, "must be https://"},
		{"file://", map[string]string{"url": `"file:///srv/app.git"`}, "must be https://"},
		{"a local path", map[string]string{"url": `"/srv/app.git"`}, "must be https://"},
		{"a user in the url", map[string]string{"url": `"https://bob@github.com/acme/app.git"`}, "must not carry credentials"},
		{"a user and password in the url", map[string]string{"url": `"https://bob:hunter2@github.com/acme/app.git"`}, "must not carry credentials"},
		{"a token in the url", map[string]string{"url": `"https://ghp_abcdef@github.com/acme/app.git"`}, "must not carry credentials"},
		{"a query", map[string]string{"url": `"https://github.com/acme/app.git?token=x"`}, "must not carry a query or a fragment"},
		{"a fragment", map[string]string{"url": `"https://github.com/acme/app.git#main"`}, "must not carry a query or a fragment"},
		{"a space", map[string]string{"url": `"https://github.com/acme/my app.git"`}, "holds a space"},
		{"a backslash", map[string]string{"url": `"https://github.com/acme\\app.git"`}, "backslash"},
		{"a host that starts with a dash", map[string]string{"url": `"https://-evil.example.com/acme/app.git"`}, "no valid host name"},
		{"an ipv6 literal", map[string]string{"url": `"https://[::1]/acme/app.git"`}, "no valid host name"},
		{"a bad port", map[string]string{"url": `"https://github.com:99999/acme/app.git"`}, "invalid port"},
		{"port 0", map[string]string{"url": `"https://github.com:0/acme/app.git"`}, "invalid port"},
		{"no path", map[string]string{"url": `"https://github.com"`}, "no repository path"},
		{"an empty path segment", map[string]string{"url": `"https://github.com/acme//app.git"`}, "empty, dot or dash-leading path segment"},
		{"a dot-dot segment", map[string]string{"url": `"https://github.com/acme/../app.git"`}, "empty, dot or dash-leading path segment"},
		{"a dash-leading segment", map[string]string{"url": `"https://github.com/acme/-app.git"`}, "empty, dot or dash-leading path segment"},
		{"a url at the limit", map[string]string{"url": `"https://github.com/` + long(MaxRepoURL-len("https://github.com/")) + `"`}, ""},
		{"a url over the limit", map[string]string{"url": `"https://github.com/` + long(MaxRepoURL) + `"`}, "repo.url is"},
		{"a control character in the url", map[string]string{"url": `"https://github.com/acme/app\u0007.git"`}, "control character"},

		// Visibility and auth.
		{"an unknown visibility", map[string]string{"visibility": `"internal"`}, "must be public or private"},
		{"an unknown auth", map[string]string{"auth": `"token"`}, "a token is never stored here"},
		{"a public repo with github_app", map[string]string{"auth": `"github_app"`}, "a public repo uses auth none"},
		{"a private repo with no auth", map[string]string{"visibility": `"private"`, "installation_id": `1`}, "a private repo uses auth github_app"},
		{"a private repo without an installation", map[string]string{"visibility": `"private"`, "auth": `"github_app"`}, "installation_id is required for a private repo"},
		{"a public repo with an installation", map[string]string{"installation_id": `5`}, "only allowed for a private repo"},
		{"installation 0", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `0`}, "from 1 to"},
		{"installation at the limit", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `9007199254740991`}, ""},
		{"installation over the limit", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `9007199254740992`}, "from 1 to"},
		{"installation with a fraction", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `1.5`}, "whole number"},
		{"installation as a string", map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `"7"`}, "whole number"},

		// Refs.
		{"a branch, a tag and a sha", map[string]string{"refs": `[{"kind":"branch","name":"release/1.x"},{"kind":"tag","name":"v1.2.3"},{"kind":"sha","name":"deadbeef"}]`}, ""},
		{"a branch and a tag of one name", map[string]string{"refs": `[{"kind":"branch","name":"v1"},{"kind":"tag","name":"v1"}]`}, ""},
		{"16 refs", map[string]string{"refs": refsN(16)}, ""},
		{"17 refs", map[string]string{"refs": refsN(17)}, "has 17 entries"},
		{"empty refs", map[string]string{"refs": `[]`}, "needs at least one ref"},
		{"a repeated ref", map[string]string{"refs": `[{"kind":"branch","name":"main"},{"kind":"branch","name":"main"}]`}, "repeats the branch"},
		{"an unknown ref kind", map[string]string{"refs": `[{"kind":"head","name":"main"}]`}, "must be branch, tag or sha"},
		{"a ref with an unknown key", map[string]string{"refs": `[{"kind":"branch","name":"main","force":true}]`}, `unknown field "force"`},
		{"a ref that is not an object", map[string]string{"refs": `["main"]`}, "must be an object"},
		{"a sha of 7", map[string]string{"refs": `[{"kind":"sha","name":"abcdef1"}]`}, ""},
		{"a sha of 64", map[string]string{"refs": `[{"kind":"sha","name":"` + strings.Repeat("a", 64) + `"}]`}, ""},
		{"a sha in capitals", map[string]string{"refs": `[{"kind":"sha","name":"ABCDEF1"}]`}, ""},
		{"a sha of 6", map[string]string{"refs": `[{"kind":"sha","name":"abcdef"}]`}, "7 to 64 hex"},
		{"a sha of 65", map[string]string{"refs": `[{"kind":"sha","name":"` + strings.Repeat("a", 65) + `"}]`}, "7 to 64 hex"},
		{"a sha that is not hex", map[string]string{"refs": `[{"kind":"sha","name":"ghijklm"}]`}, "7 to 64 hex"},
		{"a branch that starts with a dash", map[string]string{"refs": `[{"kind":"branch","name":"-main"}]`}, "not a valid git branch name"},
		{"a branch with a space", map[string]string{"refs": `[{"kind":"branch","name":"my branch"}]`}, "not a valid git branch name"},
		{"a branch with ..", map[string]string{"refs": `[{"kind":"branch","name":"a..b"}]`}, "not a valid git branch name"},
		{"a branch ending in .lock", map[string]string{"refs": `[{"kind":"branch","name":"a.lock"}]`}, "not a valid git branch name"},
		{"a component starting with a dot", map[string]string{"refs": `[{"kind":"branch","name":"a/.b"}]`}, "not a valid git branch name"},
		{"a branch ending in a slash", map[string]string{"refs": `[{"kind":"branch","name":"a/"}]`}, "not a valid git branch name"},
		{"a branch ending in a dot", map[string]string{"refs": `[{"kind":"tag","name":"a."}]`}, "not a valid git tag name"},
		{"a branch with @{", map[string]string{"refs": `[{"kind":"branch","name":"a@{b"}]`}, "not a valid git branch name"},
		{"the branch @", map[string]string{"refs": `[{"kind":"branch","name":"@"}]`}, "not a valid git branch name"},
		{"a branch with ~", map[string]string{"refs": `[{"kind":"branch","name":"a~1"}]`}, "not a valid git branch name"},
		{"a branch with a colon", map[string]string{"refs": `[{"kind":"branch","name":"a:b"}]`}, "not a valid git branch name"},
		{"a branch with a double slash", map[string]string{"refs": `[{"kind":"branch","name":"a//b"}]`}, "not a valid git branch name"},
		{"a branch with refs/ in it is just a name", map[string]string{"refs": `[{"kind":"branch","name":"refs/heads/main"}]`}, ""},
		{"a branch at the limit", map[string]string{"refs": `[{"kind":"branch","name":"` + long(MaxRepoRef) + `"}]`}, ""},
		{"a branch over the limit", map[string]string{"refs": `[{"kind":"branch","name":"` + long(MaxRepoRef+1) + `"}]`}, "repo.refs[0].name is"},

		// Dir.
		{"a nested dir", map[string]string{"dir": `"team/app"`}, ""},
		{"a dir with dots", map[string]string{"dir": `"app.v2"`}, ""},
		{"an absolute dir", map[string]string{"dir": `"/srv/app"`}, "relative path"},
		{"a home dir", map[string]string{"dir": `"~/app"`}, "relative path"},
		{"a drive dir", map[string]string{"dir": `"C:app"`}, "relative path"},
		{"a backslash dir", map[string]string{"dir": `"a\\b"`}, "relative path"},
		{"a dir with ..", map[string]string{"dir": `"../app"`}, "empty, . or .. segment"},
		{"a dir with . in the middle", map[string]string{"dir": `"a/./b"`}, "empty, . or .. segment"},
		{"a dir with an empty segment", map[string]string{"dir": `"a//b"`}, "empty, . or .. segment"},
		{"a dir with a trailing slash", map[string]string{"dir": `"a/"`}, "empty, . or .. segment"},
		{"an empty dir", map[string]string{"dir": `""`}, "repo.dir is required"},
		{"a dir at the limit", map[string]string{"dir": `"` + long(MaxRepoDir) + `"`}, ""},
		{"a dir over the limit", map[string]string{"dir": `"` + long(MaxRepoDir+1) + `"`}, "repo.dir is"},

		// Depth, submodules, enabled.
		{"depth 1000", map[string]string{"depth": `1000`}, ""},
		{"depth 1001", map[string]string{"depth": `1001`}, "from 0 to 1000"},
		{"depth -1", map[string]string{"depth": `-1`}, "from 0 to 1000"},
		{"depth as a string", map[string]string{"depth": `"1"`}, "whole number"},
		{"submodules not a bool", map[string]string{"submodules": `"yes"`}, "must be true or false"},
		{"enabled not a bool", map[string]string{"enabled": `1`}, "must be true or false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Repo, repoDoc(c.over))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Repo || it.Name == "" {
					t.Errorf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
		})
	}
}

func refsN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"kind":"branch","name":"b%d"}`, i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestParseRepoAndRepoDocument(t *testing.T) {
	r, err := ParseRepo(mustCanon(t, Repo, repoDoc(map[string]string{"visibility": `"private"`, "auth": `"github_app"`, "installation_id": `99`, "depth": `5`, "submodules": `true`, "enabled": `false`, "dir": `"x/y"`})))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "app" || r.InstallationID == nil || *r.InstallationID != 99 || r.Depth == nil || *r.Depth != 5 || r.Submodules == nil || !*r.Submodules ||
		r.IsEnabled() || r.Dir != "x/y" || len(r.Refs) != 1 || r.Refs[0] != (config.GitRef{Kind: "branch", Name: "main"}) {
		t.Fatalf("repo = %+v", r)
	}
	// A hand-written entry that leaves the defaults out exports the same bytes as one
	// that spells them, and both validate.
	bare := config.GitRepo{Name: "app", URL: "https://github.com/acme/app.git", Visibility: "public", Auth: "none", Refs: []config.GitRef{{Kind: "branch", Name: "main"}}}
	f, tr := false, true
	zero := 0
	spelled := bare
	spelled.Dir, spelled.Depth, spelled.Submodules, spelled.Enabled = "app", &zero, &f, &tr
	a, err := encodeCanonical(RepoDocument(bare))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := encodeCanonical(RepoDocument(spelled))
	if string(a) != string(b) || Hash(a) != Hash(b) {
		t.Fatalf("defaults export differently:\n%s\n%s", a, b)
	}
	if err := ValidateRepo(bare); err != nil {
		t.Fatalf("ValidateRepo: %v", err)
	}
	bad := bare
	bad.URL = "https://user:pw@github.com/acme/app.git"
	if err := ValidateRepo(bad); err == nil || !strings.Contains(err.Error(), "must not carry credentials") {
		t.Fatalf("ValidateRepo of a url with credentials: %v", err)
	}
	if _, err := ParseRepo([]byte(`{"name":"x"}`)); err == nil {
		t.Error("an incomplete document parsed")
	}
}

func mustCanon(t *testing.T, k Kind, raw []byte) []byte {
	t.Helper()
	it, err := Validate(k, raw)
	if err != nil {
		t.Fatal(err)
	}
	return it.Doc
}

func TestValidRefName(t *testing.T) {
	for _, ok := range []string{"main", "release/1.x", "v1.2.3", "feature/a-b_c", "a.b", "@a", "a@b", "UPPER"} {
		if !ValidRefName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "@", "-a", "/a", "a/", "a.", "a..b", "a@{b", "a//b", "a b", "a\tb", "a~", "a^", "a:", "a?", "a*", "a[", `a\b`, ".a", "a/.b", "a.lock", "a/b.lock/c", "a\x7f"} {
		if ValidRefName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLibraryItemsPageStatesTheRepoRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/library-items.md")), " ")
	for _, want := range []string{
		"every key but `installation_id` is required",
		"`public` requires `none` and `private` requires `github_app`",
		"1 to 16 distinct `{kind, name}`",
		"7 to 64 hex characters",
		"whole number 0 to 1000",
		"at most 256 bytes",
		"1 to 2^53 - 1",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/library-items.md does not say %q", want)
		}
	}
	if MaxRepoURL != 1024 || MaxRepoRefs != 16 || MaxRepoRef != 255 || MaxRepoDir != 256 || MaxRepoDepth != 1000 || MaxRepoInstallation != 1<<53-1 {
		t.Error("a repo limit changed; update docs/library-items.md and this test")
	}
}
