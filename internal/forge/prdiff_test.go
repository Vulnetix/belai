package forge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestGitHubDiffPR(t *testing.T) {
	f := newFake().
		on("gh pr diff 7 --color=never", "diff --git a/x b/x\n").
		on("gh pr view 7 --json headRefOid", `{"headRefOid":"`+strings.ToUpper(sha40)+`"}`)
	p, _ := For(Remote{Kind: KindGitHub}, f.run, found)
	d, err := p.DiffPR(context.Background(), "/w", 7)
	if err != nil || d.Diff != "diff --git a/x b/x" || d.HeadSHA != sha40 || d.Truncated {
		t.Fatalf("diff = %+v %v", d, err)
	}
	// An unreadable head id is dropped, not trusted.
	f = newFake().on("gh pr diff 7 --color=never", "d").on("gh pr view 7 --json headRefOid", `{"headRefOid":"main; rm -rf"}`)
	p, _ = For(Remote{Kind: KindGitHub}, f.run, found)
	if d, err = p.DiffPR(context.Background(), "/w", 7); err != nil || d.HeadSHA != "" {
		t.Fatalf("a malformed head id was kept: %+v %v", d, err)
	}
	if _, err = p.DiffPR(context.Background(), "/w", 0); err == nil {
		t.Fatal("number 0 was accepted")
	}
}

func TestGitHubDiffPRErrorAndTruncation(t *testing.T) {
	f := newFake().fail("gh pr diff 7 --color=never", errors.New("gh: no pull request"))
	p, _ := For(Remote{Kind: KindGitHub}, f.run, found)
	if _, err := p.DiffPR(context.Background(), "/w", 7); err == nil {
		t.Fatal("a failing gh gave a diff")
	}
	big := strings.Repeat("x", MaxPRDiffBytes+10)
	f = newFake().on("gh pr diff 7 --color=never", big).on("gh pr view 7 --json headRefOid", `{}`)
	p, _ = For(Remote{Kind: KindGitHub}, f.run, found)
	d, err := p.DiffPR(context.Background(), "/w", 7)
	if err != nil || !d.Truncated || len(d.Diff) != MaxPRDiffBytes {
		t.Fatalf("truncation: %d bytes truncated=%v %v", len(d.Diff), d.Truncated, err)
	}
}

func TestGitLabDiffPR(t *testing.T) {
	f := newFake().
		on("glab mr diff 3 --raw --color=never", "diff --git a/y b/y\n").
		on("glab mr view 3 -F json", `{"sha":"`+sha40+`"}`)
	p, _ := For(Remote{Kind: KindGitLab}, f.run, found)
	d, err := p.DiffPR(context.Background(), "/w", 3)
	if err != nil || d.Diff != "diff --git a/y b/y" || d.HeadSHA != sha40 {
		t.Fatalf("diff = %+v %v", d, err)
	}
}

func TestGiteaDiffPR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/acme/app/pulls/5.diff", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/plain" || r.Header.Get("Authorization") != "token tok" {
			t.Errorf("headers: %v", r.Header)
		}
		_, _ = io.WriteString(w, "diff --git a/z b/z\n")
	})
	mux.HandleFunc("/api/v1/repos/acme/app/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"number":5,"head":{"ref":"f","sha":"`+sha40+`"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { SetHosts(nil) })
	SetHosts(map[string]string{strings.Split(strings.TrimPrefix(srv.URL, "http://"), ":")[0]: "gitea"})
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
		t.Fatal(reason)
	}
	d, err := p.DiffPR(context.Background(), "", 5)
	if err != nil || d.Diff != "diff --git a/z b/z\n" || d.HeadSHA != sha40 {
		t.Fatalf("diff = %+v %v", d, err)
	}
	if _, err := p.DiffPR(context.Background(), "", 6); err == nil {
		t.Fatal("a 404 gave a diff")
	}
}
