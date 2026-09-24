package provider

import (
	"sort"
	"testing"
)

func TestRegistryCompleteness(t *testing.T) {
	for name, d := range registry {
		if d.Name == "" {
			t.Errorf("%s: descriptor has no name", name)
		}
		if d.Name != name {
			t.Errorf("%s: descriptor name %q does not match registry key", name, d.Name)
		}
		if !d.Auth.Valid() {
			t.Errorf("%s: auth %q is not valid", name, d.Auth)
		}
		if len(d.Fields) == 0 {
			t.Errorf("%s: descriptor has no fields", name)
		}
		if d.BaseURL == "" && d.BaseURLBuilder == nil {
			t.Errorf("%s: descriptor must have BaseURL or BaseURLBuilder", name)
		}
		for _, f := range d.Fields {
			if f.Name == "" {
				t.Errorf("%s: field has no name", name)
			}
			if len(f.EnvVars) == 0 {
				t.Errorf("%s.%s: field has no env vars", name, f.Name)
			}
		}
	}
}

func TestRegistryNamesStableAndRoundTrip(t *testing.T) {
	names := Names()
	if len(names) != len(registry) {
		t.Fatalf("Names() returned %d entries, want %d", len(names), len(registry))
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() is not sorted: %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate name %q", n)
		}
		seen[n] = true
		if !Builtin(n) {
			t.Fatalf("Builtin(%q) false for registry name", n)
		}
		if _, ok := Lookup(n); !ok {
			t.Fatalf("Lookup(%q) failed", n)
		}
	}
}

func TestBuiltinMatchesLookup(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "ollama", "unknown-provider"} {
		got := Builtin(name)
		_, ok := Lookup(name)
		if got != ok {
			t.Fatalf("Builtin(%q) = %v, Lookup ok = %v", name, got, ok)
		}
	}
}

func TestLocalProvidersExposeOneOptionalSecretField(t *testing.T) {
	for _, name := range []string{"ollama", "llama-server"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed", name)
		}
		var secret int
		for _, f := range d.Fields {
			if f.Secret {
				secret++
				if f.Name != "api_key" {
					t.Fatalf("%s: secret field %q is not api_key", name, f.Name)
				}
				if !f.Optional {
					t.Fatalf("%s: api_key field must be optional", name)
				}
			}
		}
		if secret != 1 {
			t.Fatalf("%s: %d secret fields, want exactly one", name, secret)
		}
	}
}

func TestTemplateMapsEveryKind(t *testing.T) {
	cases := map[string]struct {
		ok    bool
		local bool
		list  string
		surf  string
		auth  Auth
	}{
		"ollama":            {ok: true, local: true, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"llama-server":      {ok: true, local: true, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"":                  {ok: true, local: false, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"openai-compatible": {ok: true, local: false, list: "/models", surf: "openai-chat", auth: AuthBearer},
		"anthropic":         {ok: false},
		"bogus":             {ok: false},
	}
	for kind, want := range cases {
		d, ok := Template(kind)
		if ok != want.ok {
			t.Fatalf("Template(%q) ok = %v, want %v", kind, ok, want.ok)
		}
		if !want.ok {
			continue
		}
		if d.Local != want.local {
			t.Fatalf("Template(%q).Local = %v, want %v", kind, d.Local, want.local)
		}
		if d.ListPath != want.list {
			t.Fatalf("Template(%q).ListPath = %q, want %q", kind, d.ListPath, want.list)
		}
		if string(d.Surface) != want.surf {
			t.Fatalf("Template(%q).Surface = %q, want %q", kind, d.Surface, want.surf)
		}
		if d.Auth != want.auth {
			t.Fatalf("Template(%q).Auth = %q, want %q", kind, d.Auth, want.auth)
		}
	}
}

// TestStreamUsageProviders pins which providers ask for streamed token usage.
// Goal-mode accounting depends on it: without stream_options.include_usage the
// OpenAI-compatible streams never report usage and every pass counted 0.
func TestStreamUsageProviders(t *testing.T) {
	for _, name := range []string{"openai", "openrouter", "groq", "deepseek", "fireworks", "together", "xai"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("provider %q missing", name)
		}
		if !d.Usage {
			t.Errorf("provider %q must request stream usage", name)
		}
	}
}

func TestBuildCloudflareWorkersAI(t *testing.T) {
	if got := buildCloudflareWorkersAI(map[string]string{"account_id": "acct"}); got != "https://api.cloudflare.com/client/v4/accounts/acct" {
		t.Fatalf("buildCloudflareWorkersAI = %q", got)
	}
	if got := buildCloudflareWorkersAI(map[string]string{}); got != "" {
		t.Fatalf("buildCloudflareWorkersAI(no account) = %q, want empty", got)
	}
}

func TestBuildCloudflareGateway(t *testing.T) {
	// Explicit base_url wins over account_id.
	if got := buildCloudflareGateway(map[string]string{"base_url": "https://gw.example", "account_id": "acct"}); got != "https://gw.example" {
		t.Fatalf("buildCloudflareGateway = %q", got)
	}
	if got := buildCloudflareGateway(map[string]string{"account_id": "acct"}); got != "https://gateway.ai.cloudflare.com/v1/acct/default/compat" {
		t.Fatalf("buildCloudflareGateway = %q", got)
	}
	if got := buildCloudflareGateway(map[string]string{}); got != "" {
		t.Fatalf("buildCloudflareGateway(empty) = %q, want empty", got)
	}
}

func TestBuildOllama(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	if got := buildOllama(map[string]string{}); got != "http://localhost:11434/v1" {
		t.Fatalf("buildOllama(default) = %q", got)
	}
	if got := buildOllama(map[string]string{"host": "10.0.0.1", "port": "9999", "protocol": "https"}); got != "https://10.0.0.1:9999/v1" {
		t.Fatalf("buildOllama(decomposed) = %q", got)
	}

	// With no decomposition, OLLAMA_HOST is the complete prefix.
	t.Setenv("OLLAMA_HOST", "ollama.internal:11435")
	if got := buildOllama(map[string]string{}); got != "http://ollama.internal:11435/v1" {
		t.Fatalf("buildOllama(env host) = %q", got)
	}
}

func TestBuildLlamaServer(t *testing.T) {
	if got := buildLlamaServer(map[string]string{}); got != "http://localhost:8080/v1" {
		t.Fatalf("buildLlamaServer(default) = %q", got)
	}
	if got := buildLlamaServer(map[string]string{"host": "h", "port": "1234", "protocol": "https"}); got != "https://h:1234/v1" {
		t.Fatalf("buildLlamaServer(decomposed) = %q", got)
	}
}

func TestBuildGenericOpenAI(t *testing.T) {
	if got := buildGenericOpenAI(map[string]string{}); got != "http://localhost/v1" {
		t.Fatalf("buildGenericOpenAI(default) = %q", got)
	}
	if got := buildGenericOpenAI(map[string]string{"host": "h", "port": "8000", "protocol": "http"}); got != "http://h:8000/v1" {
		t.Fatalf("buildGenericOpenAI(decomposed) = %q", got)
	}
}
