package proc

import (
	"os"
	"strings"
	"testing"
)

func TestScrubbedEnvStripsSensitiveAndKeepsPlain(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "cf-id")
	t.Setenv("MY_API_KEY", "key")
	t.Setenv("MY_TOKEN", "tok")
	t.Setenv("MY_SECRET", "sec")
	t.Setenv("BELAI_SETTINGS", "/tmp")
	t.Setenv("MY_PLAIN_VAR", "keep-me")

	got := strings.Join(ScrubbedEnv(), "\n")
	for _, key := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CLOUDFLARE_ACCOUNT_ID",
		"MY_API_KEY", "MY_TOKEN", "MY_SECRET", "BELAI_SETTINGS",
	} {
		if strings.Contains(got, key+"=") {
			t.Fatalf("sensitive key %s leaked in ScrubbedEnv", key)
		}
	}
	if !strings.Contains(got, "MY_PLAIN_VAR=keep-me") {
		t.Fatalf("plain key missing from ScrubbedEnv")
	}
}

func TestScrubEnvKeepingKeepsOnlyTheNamedCredentials(t *testing.T) {
	env := []string{
		"AWS_SESSION_TOKEN=granted",
		"GH_TOKEN=not-granted",
		"OPENAI_API_KEY=sk",
		"PATH=/bin",
	}
	got := strings.Join(ScrubEnvKeeping(env, []string{"AWS_SESSION_TOKEN", "aws_session_token_lower"}), "\n")
	if !strings.Contains(got, "AWS_SESSION_TOKEN=granted") {
		t.Fatalf("a kept name was dropped: %q", got)
	}
	for _, leaked := range []string{"GH_TOKEN=", "OPENAI_API_KEY="} {
		if strings.Contains(got, leaked) {
			t.Fatalf("%s survived without being kept: %q", leaked, got)
		}
	}
	if !strings.Contains(got, "PATH=/bin") {
		t.Fatalf("an ordinary variable was dropped: %q", got)
	}
	// Names match exactly: a different case is a different variable.
	if strings.Contains(strings.Join(ScrubEnvKeeping([]string{"Aws_Session_Token=x"}, []string{"AWS_SESSION_TOKEN"}), "\n"), "Aws_Session_Token") {
		t.Fatal("a kept name must match exactly")
	}
	if got := ScrubEnvOf(env); strings.Contains(strings.Join(got, "\n"), "AWS_SESSION_TOKEN") {
		t.Fatalf("ScrubEnvOf keeps nothing extra: %q", got)
	}
}

// The Pix sandbox sets the committer to the Pix identity in the environment, so
// a commit the model makes with -c user.name still signs. Scrubbing must not
// drop it.
func TestScrubEnvOfKeepsTheCommitterIdentity(t *testing.T) {
	env := []string{
		"GIT_COMMITTER_NAME=Pix [bot]",
		"GIT_COMMITTER_EMAIL=security@vulnetix.com",
		"GIT_AUTHOR_NAME=Someone",
	}
	got := strings.Join(ScrubEnvOf(env), "\n")
	for _, want := range env {
		if !strings.Contains(got, want) {
			t.Fatalf("%q was scrubbed: %q", want, got)
		}
	}
}

// UnsetGitEnv removes only the git locator variables: a PATH that happens to
// contain a GIT_* name must survive, because the test process still needs it
// to find git. (The old implementation had the condition inverted and emptied
// the whole environment.)
func TestUnsetGitEnvKeepsTheRestOfTheEnvironment(t *testing.T) {
	t.Setenv("GIT_DIR", "/foreign/repo")
	t.Setenv("GIT_WORK_TREE", "/foreign/worktree")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/test")
	t.Setenv("GIT_COMMITTER_NAME", "Test")

	UnsetGitEnv()

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if v, ok := lookEnv(key); ok {
			t.Fatalf("%s survived UnsetGitEnv: %q", key, v)
		}
	}
	for key, want := range map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/home/test", "GIT_COMMITTER_NAME": "Test"} {
		if v, ok := lookEnv(key); !ok || v != want {
			t.Fatalf("%s = %q %v, want %q", key, v, ok, want)
		}
	}
}

// lookEnv is os.LookupEnv: a variable set to the empty string is still set.
func lookEnv(key string) (string, bool) {
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}
