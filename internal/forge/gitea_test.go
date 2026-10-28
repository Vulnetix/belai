package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKindForUsesTheForgesSetting(t *testing.T) {
	t.Cleanup(func() { SetHosts(nil) })
	SetHosts(map[string]string{"git.example.com": "gitea", "ghe.corp": "ghes", "fj.example": "Forgejo", "bad.example": "svn"})
	cases := map[string]string{
		"https://git.example.com/acme/app.git": KindGitea,
		"git@ghe.corp:acme/app.git":            KindGitHub,
		"https://fj.example/acme/app":          KindForgejo,
		"https://codeberg.org/forgejo/forgejo": KindForgejo,
		"https://bad.example/acme/app":         KindGeneric,
		"https://gitea.evil.example/acme/app":  KindGeneric,
		"https://github.com/acme/app":          KindGitHub,
		"https://gitlab.example.com/g/s/app":   KindGitLab,
	}
	for raw, want := range cases {
		rem, ok := ParseRemote(raw)
		if !ok || rem.Kind != want {
			t.Errorf("%s: kind %q, want %q", raw, rem.Kind, want)
		}
	}
}

func TestGiteaTokenNeedsANamedVariable(t *testing.T) {
	env := map[string]string{}
	old := Getenv
	Getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { Getenv = old })
	rem, _ := ParseRemote("https://codeberg.org/acme/app")
	if p, reason := For(rem, nil, func(string) (string, error) { return "", nil }); p != nil || !strings.Contains(reason, "BELAI_FORGE_TOKEN_CODEBERG_ORG") || !strings.Contains(reason, "FORGEJO_TOKEN") {
		t.Fatalf("no token: %v %q", p, reason)
	}
	env["FORGEJO_TOKEN"] = "fj"
	if tok, from := ForgeToken(KindForgejo, "codeberg.org"); tok != "fj" || from != "FORGEJO_TOKEN" {
		t.Fatalf("kind token = %q from %s", tok, from)
	}
	env["BELAI_FORGE_TOKEN_CODEBERG_ORG"] = "host"
	if tok, _ := ForgeToken(KindForgejo, "codeberg.org"); tok != "host" {
		t.Fatalf("a host token wins: %q", tok)
	}
}

func TestGiteaProvider(t *testing.T) {
	var gotAuth string
	var created map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/acme/app/pulls", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			_, _ = io.WriteString(w, `{"number":9,"html_url":"https://git.test/acme/app/pulls/9"}`)
			return
		}
		_, _ = io.WriteString(w, `[
			{"number":3,"title":"WIP: thing","state":"open","html_url":"u3","head":{"ref":"belai/K-1","repo_id":1},"base":{"repo_id":1}},
			{"number":4,"title":"fork","state":"open","html_url":"u4","head":{"ref":"belai/K-2","repo_id":2},"base":{"repo_id":1}},
			{"number":5,"title":"other","state":"open","html_url":"u5","head":{"ref":"feature","repo_id":1},"base":{"repo_id":1}}]`)
	})
	mux.HandleFunc("/api/v1/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"default_branch":"main"}`)
	})
	mux.HandleFunc("/api/v1/repos/acme/app/commits/belai/K-1/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"statuses":[{"status":"success","context":"ci/build"},{"status":"failure","context":"lint"},{"status":"pending","context":"e2e"}]}`)
	})
	mux.HandleFunc("/api/v1/repos/acme/app/issues/3/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{}`) })
	mux.HandleFunc("/api/v1/repos/acme/app/pulls/3", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("close used %s", r.Method)
		}
		_, _ = io.WriteString(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(func() { SetHosts(nil) })
	SetHosts(map[string]string{strings.Split(host, ":")[0]: "gitea"})
	old := Getenv
	Getenv = func(k string) string {
		if k == "GITEA_TOKEN" {
			return "tok"
		}
		return ""
	}
	t.Cleanup(func() { Getenv = old })

	rem, _ := ParseRemote(srv.URL + "/acme/app.git")
	p, reason := For(rem, nil, func(string) (string, error) { return "", nil })
	if p == nil {
		t.Fatalf("no provider: %s", reason)
	}
	g := p.(gitea)
	if g.base != srv.URL+"/api/v1" {
		t.Fatalf("api base %s: the origin's port must be kept", g.base)
	}
	ctx := context.Background()

	pr, err := g.PRForBranch(ctx, "", "belai/K-1")
	if err != nil || pr == nil || pr.Number != 3 || !pr.Draft || gotAuth != "token tok" {
		t.Fatalf("pr for branch = %+v %v auth %q", pr, err, gotAuth)
	}
	prs, err := g.OpenPRsWithPrefix(ctx, "", "belai/")
	if err != nil || len(prs) != 1 || prs[0].Number != 3 {
		t.Fatalf("own PRs = %+v %v (a fork's PR is never ours)", prs, err)
	}
	url, err := g.CreatePR(ctx, "", CreatePRArgs{Branch: "belai/K-7", Title: "Fix", Body: "b", Draft: true})
	if err != nil || url != "https://git.test/acme/app/pulls/9" || created["title"] != "WIP: Fix" || created["base"] != "main" || created["head"] != "belai/K-7" {
		t.Fatalf("create = %s %v %v", url, err, created)
	}
	checks, err := g.Checks(ctx, "", "belai/K-1", PR{})
	if err != nil || len(checks) != 3 || checks[0].State != CheckPass || checks[1].State != CheckFail || checks[2].State != CheckPending {
		t.Fatalf("checks = %+v %v", checks, err)
	}
	if err := g.ClosePR(ctx, "", 3, "duplicate"); err != nil {
		t.Fatal(err)
	}
	if g.Label() != "Gitea (API)" || g.Noun() != "PR" || g.Kind() != KindGitea {
		t.Fatal("labels")
	}
}
