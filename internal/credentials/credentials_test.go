package credentials

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValueRedactsInFormatting(t *testing.T) {
	v := Value{Field: "api_key", Location: "env", Source: SourceEnv, Secret: true, value: "sk-secret"}
	if strings.Contains(fmt.Sprintf("%v", v), "sk-secret") {
		t.Fatalf("%%v leaked secret")
	}
	if strings.Contains(fmt.Sprintf("%+v", v), "sk-secret") {
		t.Fatalf("%%+v leaked secret")
	}
	if strings.Contains(fmt.Sprintf("%#v", v), "sk-secret") {
		t.Fatalf("%%#v leaked secret")
	}
	if strings.Contains(fmt.Sprintf("%s", v), "sk-secret") {
		t.Fatalf("%%s leaked secret")
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(b), "sk-secret") {
		t.Fatalf("json.Marshal leaked secret: %s", b)
	}
}

func TestSetRedactsWholeStruct(t *testing.T) {
	s := Set{
		Provider: "openai",
		Values: map[string]Value{
			"api_key": {Field: "api_key", Location: "env", Source: SourceEnv, Secret: true, value: "sk-secret"},
		},
	}
	if strings.Contains(fmt.Sprintf("%+v", s), "sk-secret") {
		t.Fatalf("Set formatting leaked secret")
	}
}

func TestSpecMatchesRunResolve(t *testing.T) {
	cases := map[string][]string{
		"openai":                {"api_key"},
		"anthropic":             {"api_key"},
		"cloudflare-workers-ai": {"api_key", "account_id"},
		"cloudflare-ai-gateway": {"api_key", "upstream_api_key", "account_id", "gateway_id"},
	}
	for provider, want := range cases {
		got := Spec(provider)
		if len(got) != len(want) {
			t.Fatalf("%s: expected %d fields, got %d", provider, len(want), len(got))
		}
		for i, w := range want {
			if got[i].Name != w {
				t.Fatalf("%s: field %d = %q, want %q", provider, i, got[i].Name, w)
			}
		}
	}
}

func TestSpecUnknownProviderDoesNotUseOpenAIKey(t *testing.T) {
	spec := Spec("myprovider")
	if len(spec) != 1 {
		t.Fatalf("expected 1 field, got %d", len(spec))
	}
	for _, ev := range spec[0].EnvVars {
		if ev == "OPENAI_API_KEY" {
			t.Fatalf("unknown provider must not resolve from OPENAI_API_KEY")
		}
	}
	if len(spec[0].EnvVars) == 0 || spec[0].EnvVars[0] != "SIGNET_MYPROVIDER_API_KEY" {
		t.Fatalf("EnvVars = %v, want [SIGNET_MYPROVIDER_API_KEY]", spec[0].EnvVars)
	}
}

func TestSpecOllamaFields(t *testing.T) {
	got := Spec("ollama")
	if len(got) != 3 {
		t.Fatalf("ollama should have 3 fields, got %+v", got)
	}
	want := []string{"host", "port", "protocol"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("field %d = %q, want %q", i, got[i].Name, w)
		}
		if !got[i].Optional {
			t.Fatalf("field %q should be optional", got[i].Name)
		}
	}
}

func TestSpecLlamaServerFields(t *testing.T) {
	got := Spec("llama-server")
	if len(got) != 3 {
		t.Fatalf("llama-server should have 3 fields, got %+v", got)
	}
	want := []string{"host", "port", "protocol"}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("field %d = %q, want %q", i, got[i].Name, w)
		}
		if !got[i].Optional {
			t.Fatalf("field %q should be optional", got[i].Name)
		}
	}
}

func TestSpecHuggingFace(t *testing.T) {
	spec := Spec("huggingface")
	if len(spec) != 1 || spec[0].Name != "api_key" || !spec[0].Secret {
		t.Fatalf("huggingface spec = %+v", spec)
	}
	if spec[0].EnvVars[0] != "HF_TOKEN" {
		t.Fatalf("huggingface env = %v", spec[0].EnvVars)
	}
}
