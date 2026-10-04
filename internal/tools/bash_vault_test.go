package tools

import (
	"context"
	"os"
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
