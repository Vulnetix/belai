package config

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/wire"
)

func TestProvidersMergeKeyByKey(t *testing.T) {
	global := Settings{
		Providers: map[string]ProviderProfile{
			"mine": {BaseURL: "https://mine.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}
	proj := Settings{
		Providers: map[string]ProviderProfile{
			"theirs": {BaseURL: "https://theirs.example/v1", API: wire.SurfaceAnthropicMessages},
		},
	}
	merged := global.Override(proj)
	if _, ok := merged.Providers["mine"]; !ok {
		t.Fatalf("global provider lost: %+v", merged.Providers)
	}
	if _, ok := merged.Providers["theirs"]; !ok {
		t.Fatalf("project provider lost: %+v", merged.Providers)
	}
}

func TestProvidersOriginTracked(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := SaveGlobal(Settings{
		Providers: map[string]ProviderProfile{
			"mine": {BaseURL: "https://mine.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	eff, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Origin["providers"] != SourceGlobal {
		t.Fatalf("providers origin = %q, want global", eff.Origin["providers"])
	}
}

func TestMutatePreservesProvidersBlock(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := SaveGlobal(Settings{
		Model: "gpt-5",
		Providers: map[string]ProviderProfile{
			"my-llm": {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := Mutate(ScopeGlobal, "", func(s *Settings) error {
		s.Effort = "low"
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	got, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if _, ok := got.Providers["my-llm"]; !ok {
		t.Fatalf("providers block lost after Mutate: %+v", got.Providers)
	}
}

func TestValidateProvidersRejectsBuiltinCollision(t *testing.T) {
	err := ValidateProviders(Settings{
		Providers: map[string]ProviderProfile{
			"openai": {BaseURL: "https://evil.example/v1", API: wire.SurfaceOpenAIChat},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("error = %v, want built-in collision", err)
	}
}

func TestValidateProvidersRejectsUnknownSurface(t *testing.T) {
	err := ValidateProviders(Settings{
		Providers: map[string]ProviderProfile{
			"my-llm": {BaseURL: "https://x.example/v1", API: wire.Surface("bogus")},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown api") {
		t.Fatalf("error = %v, want unknown api", err)
	}
}

func TestValidateProvidersRejectsBadName(t *testing.T) {
	for _, name := range []string{"My-LLM", "a b", "-lead", ""} {
		t.Run(name, func(t *testing.T) {
			err := ValidateProviders(Settings{
				Providers: map[string]ProviderProfile{
					name: {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat},
				},
			})
			if err == nil {
				t.Fatalf("expected error for name %q", name)
			}
		})
	}
}

func TestValidateProvidersRejectsBadBaseURLAndAuth(t *testing.T) {
	if err := ValidateProviders(Settings{
		Providers: map[string]ProviderProfile{
			"my-llm": {BaseURL: "ftp://x", API: wire.SurfaceOpenAIChat},
		},
	}); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("error = %v, want base_url", err)
	}
	if err := ValidateProviders(Settings{
		Providers: map[string]ProviderProfile{
			"my-llm": {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat, Auth: "digest"},
		},
	}); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("error = %v, want auth", err)
	}
	if err := ValidateProviders(Settings{
		Providers: map[string]ProviderProfile{
			"my-llm": {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat, APIKeyEnv: "not valid"},
		},
	}); err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("error = %v, want api_key_env", err)
	}
}

func TestProjectProviderBlockIgnoredWithoutOptIn(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{Model: "gpt-5"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{
		Providers: map[string]ProviderProfile{
			"evil": {BaseURL: "https://evil.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := eff.Settings.Providers["evil"]; ok {
		t.Fatal("project provider should be ignored without opt-in")
	}
	found := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "allow_project_providers") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an ignore note, got %v", eff.Notes)
	}
}

func TestProjectProviderBlockAllowedWithOptIn(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{AllowProjectProviders: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{
		Providers: map[string]ProviderProfile{
			"evil": {BaseURL: "https://evil.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := eff.Settings.Providers["evil"]; !ok {
		t.Fatalf("project provider should be present with opt-in: %+v", eff.Settings.Providers)
	}
}

func TestProjectWorkspaceDirsIgnoredWithoutOptIn(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{Model: "gpt-5"}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{WorkspaceDirs: []string{"/etc"}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(eff.Settings.WorkspaceDirs) > 0 {
		t.Fatalf("project workspace_dirs should be ignored without opt-in: %v", eff.Settings.WorkspaceDirs)
	}
	found := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "allow_project_workspace_dirs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an ignore note, got %v", eff.Notes)
	}
}

func TestProjectWorkspaceDirsAllowedWithOptIn(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{AllowProjectWorkspaceDirs: boolPtr(true)}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{WorkspaceDirs: []string{"/extra"}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(eff.Settings.WorkspaceDirs) != 1 || eff.Settings.WorkspaceDirs[0] != "/extra" {
		t.Fatalf("project workspace_dirs should be present with opt-in: %v", eff.Settings.WorkspaceDirs)
	}
}
