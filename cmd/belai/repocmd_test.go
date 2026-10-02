package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/repos"
)

const cliRepoURL = "https://example.test/acme/app.git"

// cliOrigin makes a bare repository with one commit on main and points the
// command at it, through a url rewrite, so `belai repo sync` runs for real.
func cliOrigin(t *testing.T) (bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	_ = os.MkdirAll(home, 0o755)
	env := append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bare = filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	_ = os.MkdirAll(work, 0o755)
	git(root, "init", "-q", "--bare", "-b", "main", bare)
	git(work, "init", "-q", "-b", "main")
	git(work, "remote", "add", "origin", bare)
	_ = os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	git(work, "add", "a.txt")
	git(work, "commit", "-q", "-m", "first")
	git(work, "push", "-q", "origin", "main")
	old := repoOptions
	repoOptions = func() repos.Options {
		return repos.Options{Env: []string{"HOME=" + home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_ALLOW_PROTOCOL=file:https:ssh",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url." + bare + ".insteadOf", "GIT_CONFIG_VALUE_0=" + cliRepoURL}}
	}
	t.Cleanup(func() { repoOptions = old })
	return bare
}

const cliRepoDoc = `{"name":"app","url":"https://example.test/acme/app.git","visibility":"public","auth":"none","refs":[{"kind":"branch","name":"main"}],"dir":"app","depth":0,"submodules":false,"enabled":true}`

func TestRepoCLIImportListSyncStatusExport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	cliOrigin(t)

	if code, out, errOut := runLib(t, libitem.Repo, cliRepoDoc, "import", "-"); code != 0 || !strings.Contains(out, "saved") {
		t.Fatalf("import: %d %q %q", code, out, errOut)
	}
	code, out, _ := runLib(t, libitem.Repo, "", "list")
	if code != 0 || !strings.Contains(out, "app") || !strings.Contains(out, cliRepoURL) || !strings.Contains(out, "branch:main") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, _ = runLib(t, libitem.Repo, "", "status")
	if code != 0 || !strings.Contains(out, "not cloned") {
		t.Fatalf("status before sync: %d %q", code, out)
	}
	code, out, errOut := runLib(t, libitem.Repo, "", "sync")
	if code != 0 || !strings.Contains(out, "app\tcloned\tbranch:main\t") || !strings.Contains(out, filepath.Join(home, "repos", "app")) {
		t.Fatalf("sync: %d %q %q", code, out, errOut)
	}
	if b, err := os.ReadFile(filepath.Join(home, "repos", "app", "a.txt")); err != nil || string(b) != "one\n" {
		t.Fatalf("checkout: %q %v", b, err)
	}
	code, out, _ = runLib(t, libitem.Repo, "", "sync", "app")
	if code != 0 || !strings.Contains(out, "app\tcurrent\t") {
		t.Fatalf("second sync: %d %q", code, out)
	}
	code, out, _ = runLib(t, libitem.Repo, "", "status", "-json")
	var rows []struct{ Name, State, Commit string }
	if err := json.Unmarshal([]byte(out), &rows); err != nil || code != 0 || len(rows) != 1 || rows[0].State != "current" || len(rows[0].Commit) != 12 {
		t.Fatalf("status -json: %d %q %v", code, out, err)
	}
	code, out, _ = runLib(t, libitem.Repo, "", "export", "app")
	want, _ := libitem.Validate(libitem.Repo, []byte(cliRepoDoc))
	if code != 0 || out != string(want.Doc) {
		t.Fatalf("export: %d %q", code, out)
	}
	code, out, _ = runLib(t, libitem.Repo, "", "list", "-json")
	if code != 0 || !strings.Contains(out, `"name": "app"`) {
		t.Fatalf("list -json: %d %q", code, out)
	}
}

func TestRepoCLISyncReportsFailuresAndSkipsDisabled(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	cliOrigin(t)
	disabled := strings.Replace(strings.Replace(cliRepoDoc, `"name":"app"`, `"name":"off"`, 1), `"dir":"app"`, `"dir":"off"`, 1)
	disabled = strings.Replace(disabled, `"enabled":true`, `"enabled":false`, 1)
	broken := strings.Replace(strings.Replace(cliRepoDoc, `"name":"app"`, `"name":"nobranch"`, 1), `"dir":"app"`, `"dir":"nobranch"`, 1)
	broken = strings.Replace(broken, `"main"`, `"missing"`, 1)
	for _, d := range []string{cliRepoDoc, disabled, broken} {
		if code, _, errOut := runLib(t, libitem.Repo, d, "import", "-"); code != 0 {
			t.Fatalf("import: %d %q", code, errOut)
		}
	}
	code, out, errOut := runLib(t, libitem.Repo, "", "sync")
	if code != 1 || !strings.Contains(out, "app\tcloned") || !strings.Contains(out, "off\tskipped (disabled)") || !strings.Contains(errOut, "nobranch\tfailed:") || !strings.Contains(errOut, "1 of 3 repositories failed") {
		t.Fatalf("sync: %d\nout %q\nerr %q", code, out, errOut)
	}
	// Naming a disabled repository is an error, not a skip.
	if code, _, errOut := runLib(t, libitem.Repo, "", "sync", "off"); code != 1 || !strings.Contains(errOut, "is disabled") {
		t.Fatalf("sync off: %d %q", code, errOut)
	}
	if code, _, errOut := runLib(t, libitem.Repo, "", "sync", "nope"); code != 1 || !strings.Contains(errOut, `no repository named "nope"`) {
		t.Fatalf("sync nope: %d %q", code, errOut)
	}
	if code, _, _ := runLib(t, libitem.Repo, "", "sync", "a", "b"); code != 2 {
		t.Fatalf("two names: %d", code)
	}
}

func TestRepoCLIWithNothingConfigured(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	code, out, _ := runLib(t, libitem.Repo, "", "sync")
	if code != 0 || !strings.Contains(out, "no repositories") {
		t.Fatalf("sync: %d %q", code, out)
	}
	if code, out, _ := runLib(t, libitem.Repo, "", "status"); code != 0 || !strings.Contains(out, "NAME") {
		t.Fatalf("status: %d %q", code, out)
	}
	var o, e bytes.Buffer
	if code := runLibraryCLI(context.Background(), libitem.Repo, nil, strings.NewReader(""), &o, &e); code != 2 || !strings.Contains(e.String(), "sync [NAME]") || !strings.Contains(e.String(), "status [-json] [NAME]") {
		t.Fatalf("usage: %d %q", code, e.String())
	}
}

func TestRepoCLIRefusesWhatTheLibraryRefuses(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	for doc, want := range map[string]string{
		strings.Replace(cliRepoDoc, "https://example.test", "https://user:tok@example.test", 1): "must not carry credentials",
		strings.Replace(cliRepoDoc, `"auth":"none"`, `"auth":"github_app"`, 1):                  "a public repo uses auth none",
		strings.Replace(cliRepoDoc, `"main"`, `"--upload-pack=x"`, 1):                           "not a valid git branch name",
	} {
		code, _, errOut := runLib(t, libitem.Repo, doc, "import", "-")
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("%q: %d %q, want %q", doc, code, errOut, want)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("BELAI_HOME"), "settings.json")); !os.IsNotExist(err) {
		t.Error("a refused import wrote settings")
	}
}
