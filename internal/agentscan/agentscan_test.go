package agentscan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/wire"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func findFound(t *testing.T, found []Found, provider string) *Found {
	t.Helper()
	for i := range found {
		if found[i].Provider == provider {
			return &found[i]
		}
	}
	t.Fatalf("no finding for provider %q in %v", provider, found)
	return nil
}

func findNote(t *testing.T, found []Found, substr string) *Found {
	t.Helper()
	for i := range found {
		if strings.Contains(found[i].Note, substr) {
			return &found[i]
		}
	}
	t.Fatalf("no note containing %q in %v", substr, found)
	return nil
}

func TestScanPiCustomProviders(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".pi", "agent", "models.json"),
		`{"providers":{"my-llm":{"api":"openai-completions","apiKey":"sk-test","baseUrl":"https://llm.example/v1"}}}`)
	found := findFound(t, Scan(home), "my-llm")
	if found.Reveal() != "sk-test" {
		t.Fatalf("value = %q", found.Reveal())
	}
	if found.Profile == nil {
		t.Fatal("expected a profile")
	}
	if found.Profile.API != wire.SurfaceOpenAIChat || found.Profile.BaseURL != "https://llm.example/v1" {
		t.Fatalf("profile = %+v", found.Profile)
	}
	if !found.Importable() {
		t.Fatal("expected importable")
	}
}

func TestScanPiUnknownAPIDialectIsNote(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".pi", "agent", "models.json"),
		`{"providers":{"my-llm":{"api":"bogus","apiKey":"sk","baseUrl":"https://x/v1"}}}`)
	f := findNote(t, Scan(home), "unknown api dialect")
	if f.Importable() {
		t.Fatal("unknown dialect should not be importable")
	}
}

func TestScanPiCompatFlagsNoted(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".pi", "agent", "models.json"),
		`{"providers":{"my-llm":{"api":"openai-completions","apiKey":"sk","baseUrl":"https://x/v1","compat":{"supportsDeveloperRole":true}}}}`)
	f := findFound(t, Scan(home), "my-llm")
	if !strings.Contains(f.Note, "compat flags dropped") {
		t.Fatalf("note = %q, want compat flags dropped", f.Note)
	}
	if !f.Importable() {
		t.Fatal("compat flags are a warning, not an import blocker")
	}
}

func TestScanCodexEnvKeyBecomesEnvReference(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".codex", "config.toml"),
		"[model_providers.my]\nbase_url = \"https://x/v1\"\nenv_key = \"MY_KEY\"\nwire_api = \"chat\"\n")
	f := findFound(t, Scan(home), "my")
	if f.EnvKey != "MY_KEY" {
		t.Fatalf("EnvKey = %q, want MY_KEY", f.EnvKey)
	}
	if f.Profile == nil || f.Profile.APIKeyEnv != "MY_KEY" {
		t.Fatalf("profile APIKeyEnv = %+v", f.Profile)
	}
	if f.Reveal() != "" {
		t.Fatalf("env references carry no value, got %q", f.Reveal())
	}
}

func TestScanCodexOAuthIsNote(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt"}`)
	f := findNote(t, Scan(home), "auth_mode: chatgpt")
	if f.Importable() {
		t.Fatal("chatgpt auth should not be importable")
	}
}

func TestScanClaudeCodeOAuthIsNote(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":"gho_x"}`)
	f := findNote(t, Scan(home), "OAuth")
	if f.Importable() {
		t.Fatal("claude OAuth should not be importable")
	}
}

func TestScanGooseCopilotTokenMapsToProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".config", "goose", "secrets.yaml"), "GITHUB_COPILOT_TOKEN: gho_x\n")
	f := findFound(t, Scan(home), "github-copilot")
	if f.Field != "oauth_token" || f.Reveal() != "gho_x" {
		t.Fatalf("found = %+v", f)
	}
}

func TestScanGooseUnmappedSecretIsNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".config", "goose", "secrets.yaml"), "SOME_RANDOM: value\n")
	f := findNote(t, Scan(home), "not a mapped")
	if f.Importable() {
		t.Fatal("unmapped secret should not be importable")
	}
}

func TestScanOpenCodeOpenRouterMapsToBuiltin(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		`{"openrouter":{"type":"api","key":"sk-or"}}`)
	f := findFound(t, Scan(home), "openrouter")
	if f.Reveal() != "sk-or" {
		t.Fatalf("value = %q", f.Reveal())
	}
}

func TestScanSkipsOversizeFile(t *testing.T) {
	home := t.TempDir()
	big := strings.Repeat("x", maxFileSize+1)
	writeFixture(t, filepath.Join(home, ".pi", "agent", "auth.json"), big)
	found := Scan(home)
	if len(found) != 0 {
		t.Fatalf("oversize file should be skipped, got %v", found)
	}
}

func TestScanRejectsBuiltinNameCollision(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".pi", "agent", "models.json"),
		`{"providers":{"openai":{"api":"openai-completions","apiKey":"sk","baseUrl":"https://evil.example/v1"}}}`)
	f := findNote(t, Scan(home), "collides with a built-in")
	if f.Importable() {
		t.Fatal("built-in collision should not be importable")
	}
}

func TestFoundNeverPrintsSecret(t *testing.T) {
	f := Found{Agent: "pi", Provider: "my-llm", Field: "api_key", value: "sk-supersecretvalue"}
	if strings.Contains(f.String(), "sk-supersecretvalue") {
		t.Fatalf("String leaked secret: %s", f.String())
	}
	if strings.Contains(fmt.Sprintf("%#v", f), "sk-supersecretvalue") {
		t.Fatalf("GoString leaked secret: %#v", f)
	}
	if f.Mask() == "sk-supersecretvalue" {
		t.Fatalf("Mask leaked secret")
	}
}

func TestScanEmptyHomeReturnsNothing(t *testing.T) {
	if got := Scan(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty home should return nothing, got %v", got)
	}
}

func TestScanRespectsXDGDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "custom-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "custom-data"))

	writeFixture(t, filepath.Join(home, "custom-config", "goose", "secrets.yaml"), "OPENAI_API_KEY: sk-openai\n")
	writeFixture(t, filepath.Join(home, "custom-data", "opencode", "auth.json"), `{"openrouter":{"type":"api","key":"sk-or"}}`)

	found := Scan(home)
	findFound(t, found, "openai")
	findFound(t, found, "openrouter")
}
