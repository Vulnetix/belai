package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

const (
	vaultAWSKeyID  = "AKIAVAULTKEYID000001"
	vaultAWSSecret = "vault-aws-secret-access-key-value"
	vaultAWSToken  = "vault-aws-session-token-value-0001"
	vaultGHToken   = "ghs_vaultinstallationtoken000001"
)

func withVault(t *testing.T, vars ...vaultenv.Var) {
	t.Helper()
	vaultenv.Default.Replace(vars, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
}

func vaultAWS() []vaultenv.Var {
	return []vaultenv.Var{
		{Name: "AWS_ACCESS_KEY_ID", Value: vaultAWSKeyID},
		{Name: "AWS_SECRET_ACCESS_KEY", Value: vaultAWSSecret},
		{Name: "AWS_SESSION_TOKEN", Value: vaultAWSToken},
		{Name: "AWS_REGION", Value: "eu-west-2", Plain: true},
	}
}

// The AWS tool runs with the vault's keys, its session token included (it ends
// in _TOKEN, which the scrub of the launch environment would drop), and what
// the command prints of them is scrubbed. A plain variable reads as written.
func TestAWSToolGetsTheVaultKeysAndScrubsThem(t *testing.T) {
	f := newFakeAWS(t)
	t.Setenv("AWS_SESSION_TOKEN", "launch-env-session-token-0001")
	withVault(t, vaultAWS()...)
	n := nativeNamed(t, "AWS", hubWith(nil))
	res, err := n.Execute(context.Background(), logsCall(""))
	if err != nil {
		t.Fatal(err)
	}
	log := strings.Join(f.lines(t), "\n")
	if !strings.Contains(log, "key="+vaultAWSKeyID+" token="+vaultAWSToken+" region=eu-west-2") {
		t.Fatalf("the command did not run with the vault's keys:\n%s", log)
	}
	for _, secret := range []string{vaultAWSSecret, vaultAWSToken} {
		if strings.Contains(res.Content, secret) {
			t.Fatalf("a vault value reached the result:\n%s", res.Content)
		}
	}
	if !strings.Contains(res.Content, "leak: [vault:AWS_SECRET_ACCESS_KEY] [vault:AWS_SESSION_TOKEN]") {
		t.Fatalf("expected the placeholders:\n%s", res.Content)
	}
}

// An assumed role's credentials still replace the vault's AWS keys: the prepare
// step runs after the vault's variables are in place.
func TestAssumedRoleStillWinsOverTheVaultKeys(t *testing.T) {
	f := newFakeAWS(t)
	withVault(t, vaultAWS()...)
	n := nativeNamed(t, "AWS", hubWith(map[string][]string{"aws_role_arn": {testRole}}))
	if _, err := n.Execute(context.Background(), logsCall("")); err != nil {
		t.Fatal(err)
	}
	lines := f.lines(t)
	last := lines[len(lines)-1]
	if !strings.Contains(last, "key=ASIAEXAMPLEKEYID1234 token="+sessionToken) {
		t.Fatalf("the logs call must run as the assumed role:\n%s", strings.Join(lines, "\n"))
	}
	// The assume-role call itself ran from the vault's identity.
	if !strings.Contains(strings.Join(lines, "\n"), "key="+vaultAWSKeyID) {
		t.Fatalf("the role must be assumed from the vault's identity:\n%s", strings.Join(lines, "\n"))
	}
}

// The GH tool gets the vault's token and the git credential helper for
// github.com, and the token never reaches the result.
func TestGHToolGetsTheVaultTokenAndTheCredentialHelper(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"token=$GH_TOKEN\"\nenv | sort | grep '^GIT_CONFIG_'\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	withVault(t, vaultenv.Var{Name: "GH_TOKEN", Value: vaultGHToken})
	res, err := nativeNamed(t, "GH", hubWith(nil)).Execute(context.Background(), map[string]any{"command": "pr list"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, vaultGHToken) || !strings.Contains(res.Content, "token=[vault:GH_TOKEN]") {
		t.Fatalf("the token must be set and scrubbed:\n%s", res.Content)
	}
	for _, want := range []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.https://github.com.helper", "GIT_CONFIG_VALUE_0=\n",
		"GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=!gh auth git-credential",
	} {
		if !strings.Contains(res.Content+"\n", want) {
			t.Fatalf("missing %q:\n%s", want, res.Content)
		}
	}
}

// The local utilities never get the vault's variables: Env lists the scrubbed
// launch environment only.
func TestLocalNativeToolsGetNoVaultVariables(t *testing.T) {
	withVault(t, vaultenv.Var{Name: "GH_TOKEN", Value: vaultGHToken}, vaultenv.Var{Name: "AWS_REGION", Value: "eu-west-2", Plain: true})
	res, err := nativeNamed(t, "Env", hubWith(nil)).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"GH_TOKEN", vaultGHToken, "GIT_CONFIG_VALUE", "AWS_REGION=eu-west-2"} {
		if strings.Contains(res.Content, leaked) {
			t.Fatalf("%q reached a local native tool:\n%s", leaked, res.Content)
		}
	}
}

func TestEveryCloudToolTakesTheVaultEnvironment(t *testing.T) {
	for _, c := range cloudCatalog() {
		if !c.vault {
			t.Errorf("%s does not take the vault's environment", c.name)
		}
	}
	for _, c := range localCatalog() {
		if c.vault {
			t.Errorf("local tool %s must not take the vault's environment", c.name)
		}
	}
}
