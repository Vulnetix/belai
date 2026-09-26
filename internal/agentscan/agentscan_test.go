package agentscan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/wire"
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

func TestMaskShortValueRendersBareEllipsis(t *testing.T) {
	// Values shorter than 8 characters cannot reveal a 3+4 split safely, so
	// they collapse to a bare ellipsis.
	if got := (Found{value: "sk-x"}).Mask(); got != "…" {
		t.Fatalf("Mask(short) = %q, want bare ellipsis", got)
	}
	if got := (Found{value: "longer-than-8"}).Mask(); got != "lon…an-8" {
		t.Fatalf("Mask(long) = %q", got)
	}
}

func TestSortFoundsStableByProviderThenField(t *testing.T) {
	in := []Found{
		{Provider: "zeta", Field: "api_key"},
		{Provider: "alpha", Field: "oauth_token"},
		{Provider: "alpha", Field: "api_key"},
		{Provider: "alpha", Field: "api_key", Location: "/later"},
	}
	sortFounds(in)

	// Provider order first, then field order, then stable within equal keys.
	want := []string{
		"alpha/api_key", "alpha/api_key", "alpha/oauth_token", "zeta/api_key",
	}
	for i, w := range want {
		if got := in[i].Provider + "/" + in[i].Field; got != w {
			t.Fatalf("position %d = %q, want %q", i, got, w)
		}
	}
	// The two alpha/api_key entries keep their original relative order.
	if in[0].Location != "" || in[1].Location != "/later" {
		t.Fatalf("equal keys not stable: %+v", in)
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

func TestScanClaudeSettingsBothEnvKeysPreferAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFixture(t, path, `{"env":{"ANTHROPIC_API_KEY":"sk-api","ANTHROPIC_AUTH_TOKEN":"tok"}}`)
	got := scanClaudeSettings(path)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(got), got)
	}
	if got[0].Agent != "claude" || got[0].Provider != "anthropic" || got[0].Field != "api_key" {
		t.Fatalf("finding = %+v", got[0])
	}
	if got[0].Reveal() != "sk-api" {
		t.Fatalf("want ANTHROPIC_API_KEY to win, got %q", got[0].Reveal())
	}
}

func TestScanClaudeSettingsAuthTokenFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFixture(t, path, `{"env":{"ANTHROPIC_AUTH_TOKEN":"tok"}}`)
	got := scanClaudeSettings(path)
	if len(got) != 1 || got[0].Reveal() != "tok" {
		t.Fatalf("got = %v", got)
	}
}

func TestScanClaudeSettingsUnparseableIsNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFixture(t, path, `{not json`)
	got := scanClaudeSettings(path)
	if len(got) != 1 || !strings.Contains(got[0].Note, "unparseable settings.json") {
		t.Fatalf("got = %v", got)
	}
	if got[0].Importable() {
		t.Fatal("unparseable settings should not be importable")
	}
}

func TestScanClaudeCredentialsUnparseableSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	writeFixture(t, path, `{bad`)
	if got := scanClaudeCredentials(path); len(got) != 0 {
		t.Fatalf("unparseable credentials should be skipped, got %v", got)
	}
}

func TestMapPiAPIDialects(t *testing.T) {
	cases := []struct {
		in   string
		want wire.Surface
		ok   bool
	}{
		{"openai-completions", wire.SurfaceOpenAIChat, true},
		{"openai-responses", wire.SurfaceOpenAIResponses, true},
		{"anthropic-messages", wire.SurfaceAnthropicMessages, true},
		{"bogus", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := mapPiAPI(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("mapPiAPI(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestScanPiModelsAttachesStoreAndDialects(t *testing.T) {
	home := t.TempDir()
	modelsPath := filepath.Join(home, "models.json")
	storePath := filepath.Join(home, "models-store.json")
	writeFixture(t, storePath,
		`{"providers":{"my-llm":{"models":[{"id":"m1","contextWindow":8192,"maxTokens":2048}]}}}`)
	writeFixture(t, modelsPath, `{"providers":{
		"my-llm":{"api":"openai-responses","apiKey":"sk1","baseUrl":"https://a/v1"},
		"anth":{"api":"anthropic-messages","apiKey":"sk2","baseUrl":"https://b/v1"}
	}}`)

	got := scanPiModels(modelsPath, storePath)
	chat := findFound(t, got, "my-llm")
	if chat.Profile == nil || chat.Profile.API != wire.SurfaceOpenAIResponses {
		t.Fatalf("profile = %+v", chat.Profile)
	}
	if len(chat.Profile.Models) != 1 || chat.Profile.Models[0].ID != "m1" ||
		chat.Profile.Models[0].ContextWindow != 8192 || chat.Profile.Models[0].MaxTokens != 2048 {
		t.Fatalf("models = %+v", chat.Profile.Models)
	}
	anth := findFound(t, got, "anth")
	if anth.Profile == nil || anth.Profile.API != wire.SurfaceAnthropicMessages {
		t.Fatalf("profile = %+v", anth.Profile)
	}
}

func TestScanPiModelsInvalidNameIsNote(t *testing.T) {
	home := t.TempDir()
	modelsPath := filepath.Join(home, "models.json")
	writeFixture(t, modelsPath, `{"providers":{"Bad Name!":{"api":"openai-completions","apiKey":"sk","baseUrl":"https://x/v1"}}}`)
	f := findNote(t, scanPiModels(modelsPath, filepath.Join(home, "absent.json")), "invalid provider name")
	if f.Importable() {
		t.Fatal("invalid name should not be importable")
	}
}

func TestScanPiAuthBuiltinUnknownAndEmpty(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.json")
	writeFixture(t, path, `{"openai":"sk-builtin","mystery":"sk-unknown","ghost":""}`)
	got := scanPiAuth(path)

	builtin := findFound(t, got, "openai")
	if builtin.Reveal() != "sk-builtin" || !builtin.Importable() {
		t.Fatalf("builtin finding = %+v", builtin)
	}

	unknown := findNote(t, got, "is not a known provider")
	if unknown.Importable() {
		t.Fatal("unknown provider should not be importable")
	}
	if len(got) != 2 {
		t.Fatalf("empty key should be skipped, got %d findings: %v", len(got), got)
	}
}

func TestScanPiAuthUnparseableIsNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFixture(t, path, `{bad`)
	f := findNote(t, scanPiAuth(path), "unparseable auth.json")
	if f.Importable() {
		t.Fatal("unparseable auth should not be importable")
	}
}

func TestMapCodexWireAPIDialects(t *testing.T) {
	cases := []struct {
		in   string
		want wire.Surface
		ok   bool
	}{
		{"chat", wire.SurfaceOpenAIChat, true},
		{"responses", wire.SurfaceOpenAIResponses, true},
		{"messages", wire.SurfaceAnthropicMessages, true},
		{"bogus", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := mapCodexWireAPI(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("mapCodexWireAPI(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestScanCodexAuthAPIKeyString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeFixture(t, path, `{"OPENAI_API_KEY":"sk-codex"}`)
	got := scanCodexAuth(path)
	if len(got) != 1 || got[0].Provider != "openai" || got[0].Reveal() != "sk-codex" {
		t.Fatalf("got = %v", got)
	}
}

func TestScanCodexConfigUnknownWireAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFixture(t, path, "[model_providers.my]\nbase_url = \"https://x/v1\"\nwire_api = \"bogus\"\n")
	f := findNote(t, scanCodexConfig(path), "unknown wire_api")
	if f.Importable() {
		t.Fatal("unknown wire_api should not be importable")
	}
}

func TestScanCodexConfigBuiltinCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeFixture(t, path, "[model_providers.openai]\nbase_url = \"https://evil/v1\"\nwire_api = \"chat\"\n")
	f := findNote(t, scanCodexConfig(path), "collides with a built-in")
	if f.Importable() {
		t.Fatal("built-in collision should not be importable")
	}
}

func TestGooseMap(t *testing.T) {
	cases := []struct {
		in, prov, field string
		ok              bool
	}{
		{"OPENAI_API_KEY", "openai", "api_key", true},
		{"ANTHROPIC_API_KEY", "anthropic", "api_key", true},
		{"GEMINI_API_KEY", "google-gemini", "api_key", true},
		{"GOOGLE_API_KEY", "google-gemini", "api_key", true},
		{"OPENROUTER_API_KEY", "openrouter", "api_key", true},
		{"GITHUB_COPILOT_TOKEN", "github-copilot", "oauth_token", true},
		{"GH_TOKEN", "github-copilot", "oauth_token", true},
		{"SOME_RANDOM", "", "", false},
	}
	for _, c := range cases {
		prov, field, ok := gooseMap(c.in)
		if prov != c.prov || field != c.field || ok != c.ok {
			t.Fatalf("gooseMap(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, prov, field, ok, c.prov, c.field, c.ok)
		}
	}
}

func TestScanGooseUnparseableIsNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".config", "goose", "secrets.yaml"), ": : :\n")
	f := findNote(t, scanGoose(home), "unparseable secrets.yaml")
	if f.Importable() {
		t.Fatal("unparseable secrets should not be importable")
	}
}

func TestScanOpenCodeUnknownProviderIsNote(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		`{"mystery":{"type":"api","key":"sk"}}`)
	f := findNote(t, scanOpenCode(home), "is not a known provider")
	if f.Importable() {
		t.Fatal("unknown provider should not be importable")
	}
}

func TestScanOpenCodeNonAPITypeSkipped(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"),
		`{"github":{"type":"oauth","key":"x"}}`)
	if got := scanOpenCode(home); len(got) != 0 {
		t.Fatalf("non-api type should be skipped, got %v", got)
	}
}

func TestScanCopilotUnparseableIsNote(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, filepath.Join(home, ".copilot", "config.json"), `{bad`)
	f := findNote(t, scanCopilot(home), "unparseable config.json")
	if f.Importable() {
		t.Fatal("unparseable config should not be importable")
	}
}

func TestScanOtherExplainsInstalledAgents(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".gemini", ".qwen", ".aider-desk"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	if err := os.MkdirAll(filepath.Join(home, "xdg-config", "crush"), 0o700); err != nil {
		t.Fatalf("mkdir crush: %v", err)
	}
	got := scanOther(home)
	if len(got) != 4 {
		t.Fatalf("want 4 explained absences, got %d: %v", len(got), got)
	}
	for _, f := range got {
		if f.Importable() {
			t.Fatalf("explained absence should not be importable: %+v", f)
		}
	}
}
