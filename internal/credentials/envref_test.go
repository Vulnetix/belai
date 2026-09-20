package credentials

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/wire"
)

func TestWriteEnvRefRoundTrip(t *testing.T) {
	fs := newFileStore(filepath.Join(t.TempDir(), "credentials.json"), false)
	if err := fs.writeEnvRef("my-llm", "api_key", "MY_KEY"); err != nil {
		t.Fatalf("writeEnvRef: %v", err)
	}
	t.Setenv("MY_KEY", "secret-val")
	v, ok, _ := fs.read("my-llm", "api_key", Spec("my-llm"))
	if !ok {
		t.Fatal("expected env reference to resolve")
	}
	if v.Reveal() != "secret-val" {
		t.Fatalf("value = %q, want secret-val", v.Reveal())
	}
	if v.Source != SourceEnv {
		t.Fatalf("source = %q, want env", v.Source)
	}
	if v.Location != "$MY_KEY" {
		t.Fatalf("location = %q, want $MY_KEY", v.Location)
	}
}

func TestEnvRefAllowedInProjectFile(t *testing.T) {
	fs := newFileStore(filepath.Join(t.TempDir(), "credentials.json"), true)
	if err := fs.writeEnvRef("my-llm", "api_key", "MY_KEY"); err != nil {
		t.Fatalf("writeEnvRef: %v", err)
	}
	t.Setenv("MY_KEY", "project-ref-val")
	v, ok, _ := fs.read("my-llm", "api_key", Spec("my-llm"))
	if !ok {
		t.Fatalf("env reference should be allowed in a project file")
	}
	if v.Reveal() != "project-ref-val" {
		t.Fatalf("value = %q, want project-ref-val", v.Reveal())
	}
}

func TestStoreEnvRefRejectsKeychain(t *testing.T) {
	r := &Resolver{keychain: &fakeKeychain{}}
	if err := r.StoreEnvRef("openai", "api_key", "MY_KEY", SourceKeychain); err == nil {
		t.Fatal("expected keychain to reject an env reference")
	}
}

func TestStoreEnvRefValidatesName(t *testing.T) {
	r := &Resolver{userFile: newFileStore(filepath.Join(t.TempDir(), "credentials.json"), false)}
	for _, name := range []string{"9BAD", "bad-name", "bad name", ""} {
		if err := r.StoreEnvRef("openai", "api_key", name, SourceUserFile); err == nil {
			t.Fatalf("expected rejection for env name %q", name)
		}
	}
	if err := r.StoreEnvRef("openai", "api_key", "GOOD_NAME", SourceUserFile); err != nil {
		t.Fatalf("valid env name rejected: %v", err)
	}
}

func TestResolverFindsBuiltInGroqEnvKey(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-test-key")
	r := &Resolver{
		env:      os.Getenv,
		userFile: newFileStore(filepath.Join(t.TempDir(), "creds.json"), false),
		projFile: newFileStore(filepath.Join(t.TempDir(), "proj.json"), true),
		netrc:    newNetrcStore(),
		keychain: &fakeKeychain{},
	}
	v, origin, ok := r.Lookup("groq", "api_key")
	if !ok {
		t.Fatal("expected groq api_key to resolve from GROQ_API_KEY")
	}
	if v != "groq-test-key" {
		t.Fatalf("value = %q, want groq-test-key", v)
	}
	if origin != "env $GROQ_API_KEY" {
		t.Fatalf("origin = %q, want env $GROQ_API_KEY", origin)
	}
}

func TestSpecCustomProviderPrefersProfileEnvKey(t *testing.T) {
	r := &Resolver{
		settings: config.Settings{
			Providers: map[string]config.ProviderProfile{
				"my-llm": {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat, APIKeyEnv: "MY_LLM_KEY"},
			},
		},
	}
	spec := r.spec("my-llm")
	if len(spec) != 1 {
		t.Fatalf("expected 1 field, got %d", len(spec))
	}
	if len(spec[0].EnvVars) < 2 || spec[0].EnvVars[0] != "MY_LLM_KEY" || spec[0].EnvVars[1] != "SIGNET_MY_LLM_API_KEY" {
		t.Fatalf("EnvVars = %v, want [MY_LLM_KEY SIGNET_MY_LLM_API_KEY]", spec[0].EnvVars)
	}
}
