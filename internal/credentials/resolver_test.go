package credentials

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveOrderEnvWins(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "env-key")
	if err := os.WriteFile(filepath.Join(tmp, "user.json"), []byte(`{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"user-key"}}}}`), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	r := &Resolver{
		env:      os.Getenv,
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(filepath.Join(tmp, "user.json"), false),
		projFile: newFileStore(filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json"), true),
		netrc:    &netrcStore{path: filepath.Join(tmp, ".netrc")},
		keychain: &fakeKeychain{},
	}
	set := r.Resolve("openai")
	if set.Values["api_key"].Source != SourceEnv {
		t.Fatalf("env should win, got %q", set.Values["api_key"].Source)
	}
	if set.Values["api_key"].Reveal() != "env-key" {
		t.Fatalf("value = %q", set.Values["api_key"].Reveal())
	}
}

func TestResolveOrderProjectFileWinsOverUserFile(t *testing.T) {
	tmp := t.TempDir()
	userPath := filepath.Join(tmp, "user.json")
	projPath := filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(userPath, []byte(`{"version":1,"providers":{"cloudflare-workers-ai":{"account_id":{"source":"inline","value":"user-acct"}}}}`), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(projPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(projPath, []byte(`{"version":1,"providers":{"cloudflare-workers-ai":{"account_id":{"source":"inline","value":"proj-acct"}}}}`), 0o600); err != nil {
		t.Fatalf("write proj file: %v", err)
	}
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(userPath, false),
		projFile: newFileStore(projPath, true),
		netrc:    &netrcStore{path: filepath.Join(tmp, ".netrc")},
		keychain: &fakeKeychain{},
	}
	set := r.Resolve("cloudflare-workers-ai")
	if set.Values["account_id"].Source != SourceProjectFile {
		t.Fatalf("project file should win, got %q", set.Values["account_id"].Source)
	}
	if set.Values["account_id"].Reveal() != "proj-acct" {
		t.Fatalf("value = %q", set.Values["account_id"].Reveal())
	}
}

func TestResolveOrderUserFileWinsOverNetrc(t *testing.T) {
	tmp := t.TempDir()
	userPath := filepath.Join(tmp, "user.json")
	netrcPath := filepath.Join(tmp, ".netrc")
	if err := os.WriteFile(userPath, []byte(`{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"user-key"}}}}`), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	if err := os.WriteFile(netrcPath, []byte(`machine api.openai.com password netrc-key`), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(userPath, false),
		projFile: newFileStore(filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json"), true),
		netrc:    &netrcStore{path: netrcPath},
		keychain: &fakeKeychain{},
	}
	set := r.Resolve("openai")
	if set.Values["api_key"].Source != SourceUserFile {
		t.Fatalf("user file should win, got %q", set.Values["api_key"].Source)
	}
	if set.Values["api_key"].Reveal() != "user-key" {
		t.Fatalf("value = %q", set.Values["api_key"].Reveal())
	}
}

func TestResolveOrderNetrcWinsOverKeychain(t *testing.T) {
	tmp := t.TempDir()
	netrcPath := filepath.Join(tmp, ".netrc")
	if err := os.WriteFile(netrcPath, []byte(`machine api.openai.com password netrc-key`), 0o600); err != nil {
		t.Fatalf("write netrc: %v", err)
	}
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(filepath.Join(tmp, "user.json"), false),
		projFile: newFileStore(filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json"), true),
		netrc:    &netrcStore{path: netrcPath},
		keychain: &fakeKeychain{data: map[string]string{"openai:api_key": "kc-key"}},
	}
	set := r.Resolve("openai")
	if set.Values["api_key"].Source != SourceNetrc {
		t.Fatalf("netrc should win, got %q", set.Values["api_key"].Source)
	}
	if set.Values["api_key"].Reveal() != "netrc-key" {
		t.Fatalf("value = %q", set.Values["api_key"].Reveal())
	}
}

func TestResolveOrderKeychainLast(t *testing.T) {
	tmp := t.TempDir()
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(filepath.Join(tmp, "user.json"), false),
		projFile: newFileStore(filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json"), true),
		netrc:    &netrcStore{path: filepath.Join(tmp, ".netrc")},
		keychain: &fakeKeychain{data: map[string]string{"openai:api_key": "kc-key"}},
	}
	set := r.Resolve("openai")
	if set.Values["api_key"].Source != SourceKeychain {
		t.Fatalf("keychain should win, got %q", set.Values["api_key"].Source)
	}
	if set.Values["api_key"].Reveal() != "kc-key" {
		t.Fatalf("value = %q", set.Values["api_key"].Reveal())
	}
}

func TestResolveIncompleteReportsMissing(t *testing.T) {
	tmp := t.TempDir()
	userPath := filepath.Join(tmp, "user-credentials.json")
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(userPath, []byte(`{"version":1,"providers":{"cloudflare-workers-ai":{"api_key":{"source":"inline","value":"k"}}}}`), 0o600); err != nil {
		t.Fatalf("write user file: %v", err)
	}
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  filepath.Join(tmp, "proj"),
		userFile: newFileStore(userPath, false),
		projFile: newFileStore(filepath.Join(tmp, "proj", ".vulnetix", "signet", "credentials.json"), true),
		netrc:    &netrcStore{path: filepath.Join(tmp, "no-netrc")},
		keychain: &fakeKeychain{},
	}
	set := r.Resolve("cloudflare-workers-ai")
	if set.Complete() {
		t.Fatalf("expected incomplete set")
	}
	if len(set.Missing) != 1 || set.Missing[0] != "account_id" {
		t.Fatalf("missing = %v, want [account_id]", set.Missing)
	}
}

func TestKeychainTimeoutTreatedAsUnavailable(t *testing.T) {
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  t.TempDir(),
		userFile: newFileStore(filepath.Join(t.TempDir(), "user.json"), false),
		projFile: newFileStore(filepath.Join(t.TempDir(), "proj.json"), true),
		netrc:    &netrcStore{path: filepath.Join(t.TempDir(), "no-netrc")},
		keychain: &fakeKeychain{sleep: 10 * time.Second},
	}
	set := r.Resolve("openai")
	if set.Complete() {
		t.Fatalf("expected incomplete set")
	}
	if len(set.Missing) != 1 || set.Missing[0] != "api_key" {
		t.Fatalf("missing = %v, want [api_key]", set.Missing)
	}
}

func TestRedactScrubsProviderErrorBody(t *testing.T) {
	secret := "sk-abc123"
	body := `{"error":{"message":"invalid key sk-abc123"}}`
	got := Redact(body, []string{secret})
	if got != `{"error":{"message":"invalid key <redacted>"}}` {
		t.Fatalf("redaction failed: %q", got)
	}
}

// countingKeychain records how many times Available is probed.
type countingKeychain struct {
	fakeKeychain
	calls int
}

func (c *countingKeychain) Available() bool {
	c.calls++
	return true
}

func TestKeychainAvailabilityProbedOnce(t *testing.T) {
	kc := &countingKeychain{}
	r := &Resolver{
		env:      func(string) string { return "" },
		workdir:  t.TempDir(),
		userFile: newFileStore(filepath.Join(t.TempDir(), "user.json"), false),
		projFile: newFileStore(filepath.Join(t.TempDir(), "proj.json"), true),
		netrc:    &netrcStore{path: filepath.Join(t.TempDir(), "no-netrc")},
		keychain: kc,
	}
	// Resolve, Lookup and Backends all touch the keychain path.
	_ = r.Resolve("openai")
	_, _, _ = r.Lookup("anthropic", "api_key")
	_ = r.Backends()

	if kc.calls != 1 {
		t.Fatalf("keychain.Available called %d times, want 1", kc.calls)
	}
}
