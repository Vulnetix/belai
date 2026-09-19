package vulnetixcli

import "testing"

func TestParseEnv(t *testing.T) {
	text := "API\nAPI URL: https://api.example.com/v1\n\nWEB\nWeb URL: https://example.com\n\nGIT\nRemote: git@github.com:foo/bar.git\nBranch: main\n\nPACKAGE MANAGERS\nnpm: installed\npy: disabled\n"
	info := ParseEnv(text)
	if info.APIURL != "https://api.example.com/v1" {
		t.Fatalf("APIURL = %q", info.APIURL)
	}
	if info.WebURL != "https://example.com" {
		t.Fatalf("WebURL = %q", info.WebURL)
	}
	if info.GitRemote != "git@github.com:foo/bar.git" {
		t.Fatalf("GitRemote = %q", info.GitRemote)
	}
	if info.GitBranch != "main" {
		t.Fatalf("GitBranch = %q", info.GitBranch)
	}
	if len(info.PackageManagers) != 1 || info.PackageManagers[0] != "npm" {
		t.Fatalf("PackageManagers = %v", info.PackageManagers)
	}
}

func TestResolveURLs(t *testing.T) {
	getenv := func(k string) string {
		if k == "VULNETIX_WEB_URL" {
			return "https://app.example.com"
		}
		return ""
	}
	authenticated, err := ResolveURLs(getenv, true)
	if err != nil {
		t.Fatalf("ResolveURLs: %v", err)
	}
	if authenticated.Dashboard == "" {
		t.Fatal("expected dashboard URL when authenticated")
	}
	if authenticated.Register != "" {
		t.Fatal("expected no register URL when authenticated")
	}

	unauth, err := ResolveURLs(getenv, false)
	if err != nil {
		t.Fatalf("ResolveURLs: %v", err)
	}
	if unauth.Register == "" {
		t.Fatal("expected register URL when unauthenticated")
	}
}

func TestResolveURLsRejectsBadSchemes(t *testing.T) {
	getenv := func(k string) string {
		if k == "VULNETIX_WEB_URL" {
			return "file:///etc/passwd"
		}
		return ""
	}
	if _, err := ResolveURLs(getenv, false); err == nil {
		t.Fatal("expected error for file:// URL")
	}
}
