package credentials

import (
	"strings"
	"testing"
)

func TestMCPSecretRoundTripKeychainThenFile(t *testing.T) {
	kc := &fakeKeychain{data: map[string]string{}}
	r := newBareResolver(t.TempDir(), nil, kc)
	if r.HasMCPSecret("github", "token") {
		t.Fatal("nothing stored yet")
	}
	if err := r.StoreMCPSecret("github", "token", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if kc.data["mcp:github:token"] != "s3cret" {
		t.Fatalf("the preferred backend is the keychain: %v", kc.data)
	}
	if v, err := r.MCPSecret("github", "token"); err != nil || v != "s3cret" {
		t.Fatalf("read back: %q %v", v, err)
	}
	// Another server's secret of the same key is separate.
	if r.HasMCPSecret("other", "token") {
		t.Fatal("secrets are per server")
	}
	r.ClearMCPSecret("github", "token")
	if r.HasMCPSecret("github", "token") || len(kc.data) != 0 {
		t.Fatalf("cleared: %v", kc.data)
	}
}

func TestMCPSecretInTheUserFileWhenThereIsNoKeychain(t *testing.T) {
	r := newBareResolver(t.TempDir(), nil, &fakeKeychain{data: map[string]string{}, broken: true})
	if err := r.StoreMCPSecret("docs", "API_KEY", "abc"); err != nil {
		t.Fatal(err)
	}
	if v, err := r.MCPSecret("docs", "API_KEY"); err != nil || v != "abc" {
		t.Fatalf("read back: %q %v", v, err)
	}
	r.ClearMCPSecret("docs", "API_KEY")
	if r.HasMCPSecret("docs", "API_KEY") {
		t.Fatal("cleared")
	}
}

// The environment is never a source: a variable named like the credential does
// not supply it, so a repository or a shell cannot shadow a stored secret.
func TestMCPSecretIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("BELAI_MCP_GITHUB_TOKEN", "from-env")
	t.Setenv("TOKEN", "from-env")
	r := newBareResolver(t.TempDir(), nil, &fakeKeychain{data: map[string]string{}, broken: true})
	if r.HasMCPSecret("github", "TOKEN") {
		t.Fatal("the environment must not supply an MCP secret")
	}
}

func TestStoreMCPSecretRefusesBadInput(t *testing.T) {
	r := newBareResolver(t.TempDir(), nil, &fakeKeychain{data: map[string]string{}, broken: true})
	for name, tc := range map[string]struct{ server, key, secret string }{
		"server":  {"has space", "k", "v"},
		"key":     {"s", "1bad", "v"},
		"empty":   {"s", "k", ""},
		"control": {"s", "k", "a\nb"},
		"huge":    {"s", "k", strings.Repeat("x", 5000)},
	} {
		if err := r.StoreMCPSecret(tc.server, tc.key, tc.secret); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
