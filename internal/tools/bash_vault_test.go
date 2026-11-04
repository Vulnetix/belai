package tools

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

const vaultTestSecret = "vault-test-secret-value-42"

func withVaultVar(t *testing.T) {
	t.Helper()
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "VAULT_TEST_TOKEN", Value: vaultTestSecret}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
}

func TestBashGetsTheVaultVariableAndNeverPrintsItsValue(t *testing.T) {
	withVaultVar(t)
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `printf 'len=%s\n' "${#VAULT_TEST_TOKEN}"; echo "token is $VAULT_TEST_TOKEN"; env | grep VAULT_TEST`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "len=26") {
		t.Fatalf("the command must see the real value (length 26): %q", res.Content)
	}
	if strings.Contains(res.Content, vaultTestSecret) {
		t.Fatalf("the value reached the tool result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "token is [vault:VAULT_TEST_TOKEN]") || !strings.Contains(res.Content, "VAULT_TEST_TOKEN=[vault:VAULT_TEST_TOKEN]") {
		t.Fatalf("expected the placeholder in both lines: %q", res.Content)
	}
}

func TestBashScrubsTheStreamedLinesToo(t *testing.T) {
	withVaultVar(t)
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	var seen []string
	_, err := b.ExecuteStream(context.Background(), map[string]any{"command": `echo "$VAULT_TEST_TOKEN"`}, func(p Progress) { seen = append(seen, p.Text) })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range seen {
		if strings.Contains(line, vaultTestSecret) {
			t.Fatalf("a streamed line leaked the value: %q", line)
		}
	}
}

func TestBashScrubsAnEncodedFormOfTheValue(t *testing.T) {
	withVaultVar(t)
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `printf '%s' "$VAULT_TEST_TOKEN" | base64`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "dmF1bHQtdGVzdC1zZWNyZXQtdmFsdWUtNDI") {
		t.Fatalf("the base64 form leaked: %q", res.Content)
	}
}

func TestVaultVariableIsNotInBelaisOwnEnvironment(t *testing.T) {
	withVaultVar(t)
	if v, ok := os.LookupEnv("VAULT_TEST_TOKEN"); ok {
		t.Fatalf("the harness's own environment must not hold it: %q", v)
	}
}

func TestReadOnlyBashGetsNoVaultVariables(t *testing.T) {
	withVaultVar(t)
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "VAULT_TEST_TOKEN") || strings.Contains(res.Content, vaultTestSecret) {
		t.Fatalf("the read-only gate must not receive vault variables: %q", res.Content)
	}
}

func TestBashDescriptionNamesTheVariablesNotTheValues(t *testing.T) {
	withVaultVar(t)
	d := (&Bash{}).Definition().Description
	if !strings.Contains(d, "VAULT_TEST_TOKEN") || strings.Contains(d, vaultTestSecret) {
		t.Fatalf("the model is told the names and never the values: %q", d)
	}
}

func TestAnExpiredVariableIsNotInjected(t *testing.T) {
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "VAULT_TEST_TOKEN", Value: vaultTestSecret, ExpiresAt: time.Now().Add(-time.Minute)}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `echo "set=${VAULT_TEST_TOKEN:-no}"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "set=no") {
		t.Fatalf("an expired variable must not be set: %q", res.Content)
	}
}

// With a GitHub token in the vault, full Bash gets the git credential helper for
// github.com, so git clones a private repository through gh; git reads it back.
func TestBashGetsTheGitCredentialHelperWithAVaultGitHubToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "GITHUB_TOKEN", Value: vaultTestSecret}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.directory")
	t.Setenv("GIT_CONFIG_VALUE_0", "*")
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `echo "count=$GIT_CONFIG_COUNT"; git config --get-all credential.https://github.com.helper | sed 's/^/helper:/'; git config --get safe.directory`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"count=3", "helper:\n", "helper:!gh auth git-credential", "\n*"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("missing %q: %q", want, res.Content)
		}
	}
}

func TestBashGetsNoCredentialHelperWithoutAGitHubToken(t *testing.T) {
	withVaultVar(t)
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `env | grep -c '^GIT_CONFIG_KEY_.*credential' || true`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Content) != "0" {
		t.Fatalf("no helper without a GitHub token: %q", res.Content)
	}
}

func TestReadOnlyBashGetsNoCredentialHelper(t *testing.T) {
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "GH_TOKEN", Value: vaultTestSecret}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "gh auth git-credential") || strings.Contains(res.Content, "GH_TOKEN") {
		t.Fatalf("read-only Bash must get neither the token nor the helper: %q", res.Content)
	}
}

// A vault-granted AWS_SESSION_TOKEN reaches the command although its name ends
// in _TOKEN; a launch-environment token the vault does not grant does not.
func TestBashKeepsAVaultGrantedSessionToken(t *testing.T) {
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "AWS_SESSION_TOKEN", Value: vaultTestSecret}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
	t.Setenv("OTHER_TOKEN", "launch-env-other-token")
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `echo "len=${#AWS_SESSION_TOKEN} other=${OTHER_TOKEN:-unset}"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "len=26 other=unset") {
		t.Fatalf("%q", res.Content)
	}
}

// A plain variable reaches the command and reads as written in its output.
func TestBashPrintsAPlainVariableAsWritten(t *testing.T) {
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "AWS_DEFAULT_REGION", Value: "us-east-1", Plain: true}}, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `echo "region=$AWS_DEFAULT_REGION"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "region=us-east-1") {
		t.Fatalf("%q", res.Content)
	}
}

// The commit signer's refusal is followed by one harness line that tells the
// model to leave the committer alone.
func TestBashExplainsTheNotPixIdentityRefusal(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": `echo "pix-git-sign: not signed (not_pix_identity)" >&2; exit 1`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Content, pixIdentityHint) != 1 || !strings.HasSuffix(res.Content, "\n"+pixIdentityHint) {
		t.Fatalf("want the hint once, last: %q", res.Content)
	}
	if !strings.Contains(pixIdentityHint, "Pix [bot] <security@vulnetix.com>") || !strings.Contains(pixIdentityHint, "GIT_AUTHOR_*") {
		t.Fatalf("hint text: %q", pixIdentityHint)
	}
	res, err = b.Execute(context.Background(), map[string]any{"command": "echo committed"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, pixIdentityHint) {
		t.Fatalf("no hint without the refusal: %q", res.Content)
	}
}
