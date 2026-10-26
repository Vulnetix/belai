package repos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// adoptedClone is a checkout outside any repos directory whose origin is the
// test url, as a user's own clone would be.
func adoptedClone(t *testing.T, o *origin) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mine", "app")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	o.git(filepath.Dir(dir), "-c", "url."+o.bare+".insteadOf="+testURL, "clone", "-q", testURL, dir)
	return dir
}

// Fetch works on a checkout anywhere: branches land in refs/remotes/origin/*,
// the checked out branch and the work tree are left alone.
func TestFetchUpdatesRemoteTrackingRefsOnly(t *testing.T) {
	o := newOrigin(t)
	dir := adoptedClone(t, o)
	before := headOf(o, dir)
	o.commit("b.txt", "two\n", "second")
	o.push("main")
	o.git(o.work, "checkout", "-q", "-b", "feature")
	tip := o.commit("c.txt", "three\n", "third")
	o.push("feature")
	ctx := context.Background()
	if err := Fetch(ctx, o.opts(""), dir, testURL, "main", "feature"); err != nil {
		t.Fatal(err)
	}
	if got := o.git(dir, "rev-parse", "refs/remotes/origin/feature"); got != tip {
		t.Errorf("origin/feature = %s, want %s", got, tip)
	}
	if got := headOf(o, dir); got != before {
		t.Error("Fetch moved the checked out branch")
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); !os.IsNotExist(err) {
		t.Error("Fetch changed the work tree")
	}
	if out, err := Git(ctx, o.opts(""), dir, "show-ref", "--verify", "-q", "refs/heads/feature"); err == nil {
		t.Errorf("Fetch made a local branch: %q", out)
	}
}

func TestFetchRefusesWhatItCannotTrust(t *testing.T) {
	o := newOrigin(t)
	dir := adoptedClone(t, o)
	ctx := context.Background()
	link := filepath.Join(t.TempDir(), "link")
	if runtime.GOOS != "windows" {
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, dir, url string
		branches       []string
		want           string
	}{
		{"relative dir", "mine/app", testURL, []string{"main"}, "absolute"},
		{"missing dir", filepath.Join(dir, "nope"), testURL, []string{"main"}, "cannot be read"},
		{"another origin", dir, "https://example.test/other/app.git", []string{"main"}, "has origin"},
		{"file url", dir, "file://" + o.bare, []string{"main"}, "https or ssh"},
		{"password in url", dir, "https://u:p@example.test/acme/app.git", []string{"main"}, "https or ssh"},
		{"option as branch", dir, testURL, []string{"--upload-pack=x"}, "not a branch"},
		{"dot dot branch", dir, testURL, []string{"a..b"}, "not a branch"},
		{"no branch", dir, testURL, nil, "no branch"},
	} {
		err := Fetch(ctx, o.opts(""), c.dir, c.url, c.branches...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if runtime.GOOS != "windows" {
		if err := Fetch(ctx, o.opts(""), link, testURL, "main"); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("symlink: %v", err)
		}
	}
	// A branch origin does not have fails with git's own reason.
	if err := Fetch(ctx, o.opts(""), dir, testURL, "nope"); err == nil || !strings.Contains(err.Error(), "couldn't find remote ref") {
		t.Errorf("missing branch: %v", err)
	}
}

// Git runs hardened: no hooks, no credential helper, the scrubbed environment.
func TestGitIsHardenedAndCarriesNoCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix hooks")
	}
	o := newOrigin(t)
	dir := adoptedClone(t, o)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "post-checkout"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{ghOK: true}
	opts := o.opts("")
	opts.Run = rec.run
	opts.GitHubCLI = true
	opts.LookPath = func(n string) (string, error) {
		if n == "git" {
			return exec.LookPath("git")
		}
		return "/usr/bin/gh", nil
	}
	t.Setenv("GH_TOKEN", "ghp_secret")
	if _, err := Git(context.Background(), opts, dir, "checkout", "-q", "-b", "side"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a repository hook ran")
	}
	for _, c := range rec.calls {
		if filepath.Base(c.argv[0]) == "gh" {
			t.Error("Git consulted gh")
		}
		if !hasArg(c, "credential.helper=") || !hasArg(c, "core.hooksPath="+os.DevNull) {
			t.Errorf("not hardened: %v", c.argv)
		}
		if hasEnv(c, "GH_TOKEN=") {
			t.Error("a token reached Git")
		}
	}
	if _, err := Git(context.Background(), opts, "relative", "status"); err == nil {
		t.Error("a relative dir was accepted")
	}
	if _, err := Git(context.Background(), opts, dir); err == nil {
		t.Error("an empty command was accepted")
	}
}

// Fetch takes the GitHub CLI's helper when asked and gh is signed in, and still
// fetches without gh.
func TestFetchUsesTheGitHubCLIBestEffort(t *testing.T) {
	o := newOrigin(t)
	dir := adoptedClone(t, o)
	helper := "credential.https://example.test.helper=!gh auth git-credential"
	for _, signedIn := range []bool{true, false} {
		rec := &recorder{ghOK: signedIn}
		opts := o.opts("")
		opts.Run = rec.run
		opts.GitHubCLI = true
		opts.LookPath = func(n string) (string, error) {
			if n == "git" {
				return exec.LookPath("git")
			}
			return "/usr/bin/gh", nil
		}
		if err := Fetch(context.Background(), opts, dir, testURL, "main"); err != nil {
			t.Fatalf("signed in %v: %v", signedIn, err)
		}
		saw := false
		for _, c := range rec.git() {
			saw = saw || hasArg(c, helper)
		}
		if saw != signedIn {
			t.Errorf("signed in %v: helper used %v", signedIn, saw)
		}
	}
	opts := o.opts("")
	opts.GitHubCLI = true
	opts.LookPath = func(n string) (string, error) {
		if n == "git" {
			return exec.LookPath("git")
		}
		return "", errors.New("not found")
	}
	if err := Fetch(context.Background(), opts, dir, testURL, "main"); err != nil {
		t.Fatalf("no gh: %v", err)
	}
}

func TestFetchURL(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/acme/api.git":   true,
		"git@github.com:acme/api.git":       true,
		"ssh://git@github.com/acme/api.git": true,
		"ssh://git:pw@github.com/acme/api":  false,
		"https://tok@github.com/acme/api":   false,
		"http://github.com/acme/api":        false,
		"file:///tmp/x":                     false,
		"git@github.com:-x":                 false,
		"git@github.com":                    false,
		"https://github.com/acme/a b":       false,
		"":                                  false,
	} {
		if got := fetchURL(url); got != want {
			t.Errorf("fetchURL(%q) = %v", url, got)
		}
	}
}
