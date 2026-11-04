package forge

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

func withVaultGitHubToken(t *testing.T, vars ...vaultenv.Var) {
	t.Helper()
	vaultenv.Default.Replace(vars, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
}

func TestRemoteAuthStillPrefersGHTokenAndSetsTheHelper(t *testing.T) {
	withVaultGitHubToken(t,
		vaultenv.Var{Name: "GITHUB_TOKEN", Value: "github-token-value"},
		vaultenv.Var{Name: "GH_TOKEN", Value: "gh-token-value-1"},
	)
	env, args := RemoteAuth(Remote{Host: "github.com", Kind: KindGitHub}, time.Now())
	if want := []string{"GH_TOKEN=gh-token-value-1", "GITHUB_TOKEN=gh-token-value-1"}; !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
	want := []string{"-c", "credential.https://github.com.helper=", "-c", "credential.https://github.com.helper=!gh auth git-credential"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestRemoteAuthEnvWithoutATokenChangesNothing(t *testing.T) {
	withVaultGitHubToken(t, vaultenv.Var{Name: "NPM_TOKEN", Value: "npm-token-value-1"})
	env := []string{"PATH=/bin", "GIT_CONFIG_COUNT=1"}
	if got := RemoteAuthEnv(env, time.Now()); !reflect.DeepEqual(got, env) {
		t.Fatalf("got %v, want env unchanged", got)
	}
}

func TestRemoteAuthEnvStartsAtZero(t *testing.T) {
	withVaultGitHubToken(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: "github-token-value"})
	got := RemoteAuthEnv([]string{"PATH=/bin"}, time.Now())
	want := []string{
		"PATH=/bin",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=!gh auth git-credential",
		"GIT_CONFIG_COUNT=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if strings.Contains(strings.Join(got, "\n"), "github-token-value") {
		t.Fatal("the token must not be added by the helper entries")
	}
}

func TestRemoteAuthEnvAppendsAfterExistingEntries(t *testing.T) {
	withVaultGitHubToken(t, vaultenv.Var{Name: "GH_TOKEN", Value: "gh-token-value-1"})
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=*",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_1=core.pager", "GIT_CONFIG_VALUE_1=cat",
	}
	got := RemoteAuthEnv(env, time.Now())
	counts := 0
	for _, e := range got {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			counts++
			if e != "GIT_CONFIG_COUNT=4" {
				t.Fatalf("count = %q, want 4", e)
			}
		}
	}
	if counts != 1 {
		t.Fatalf("want exactly one GIT_CONFIG_COUNT, got %d in %v", counts, got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_KEY_1=core.pager",
		"GIT_CONFIG_KEY_2=credential.https://github.com.helper", "GIT_CONFIG_VALUE_2=\n",
		"GIT_CONFIG_VALUE_3=!gh auth git-credential",
	} {
		if !strings.Contains(joined+"\n", want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

// git itself reads the entries: the helper list for github.com is reset and then
// holds gh alone.
func TestRemoteAuthEnvIsWhatGitReads(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withVaultGitHubToken(t, vaultenv.Var{Name: "GH_TOKEN", Value: "gh-token-value-1"})
	env := RemoteAuthEnv([]string{"HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=x"}, time.Now())
	cmd := exec.Command("git", "config", "--get-all", "credential.https://github.com.helper")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	if got := string(out); got != "\n!gh auth git-credential\n" {
		t.Fatalf("git read %q", got)
	}
	cmd = exec.Command("git", "config", "--get", "user.name")
	cmd.Env = env
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "x" {
		t.Fatalf("the existing entry was lost: %q %v", out, err)
	}
}
