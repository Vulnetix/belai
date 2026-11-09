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

// UnsetGitEnv must unset the git locator variables and leave the rest of the
// environment alone: fleet's TestMain runs it for the whole package, so
// unsetting the complement would take PATH away from every git subprocess.
func TestUnsetGitEnvDropsOnlyTheGitLocators(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GIT_INDEX_FILE", "/elsewhere/.git/index")
	t.Setenv("PATH", "/kept:/bin")

	UnsetGitEnv()

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Errorf("%s survived UnsetGitEnv", key)
		}
	}
	if got := os.Getenv("PATH"); got != "/kept:/bin" {
		t.Errorf("PATH = %q, want it kept", got)
	}
}
