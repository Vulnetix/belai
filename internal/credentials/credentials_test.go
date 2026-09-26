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
		"cloudflare-ai-gateway": {"token", "account_id", "base_url"},
		"groq":                  {"api_key", "base_url"},
		"deepseek":              {"api_key", "base_url"},
		"fireworks":             {"api_key", "base_url"},
		"mistral":               {"api_key", "base_url"},
		"together":              {"api_key", "base_url"},
		"xai":                   {"api_key", "base_url"},
		"moonshot":              {"api_key", "base_url"},
		"minimax":               {"api_key", "base_url"},
		"alibaba":               {"api_key", "base_url"},
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
	if len(spec[0].EnvVars) == 0 || spec[0].EnvVars[0] != "BELAI_MYPROVIDER_API_KEY" {
		t.Fatalf("EnvVars = %v, want [BELAI_MYPROVIDER_API_KEY]", spec[0].EnvVars)
	}
}

func TestSpecOllamaFields(t *testing.T) {
	got := Spec("ollama")
	if len(got) != 4 {
		t.Fatalf("ollama should have 4 fields, got %+v", got)
	}
	want := []string{"host", "port", "protocol", "api_key"}
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
	if len(got) != 4 {
		t.Fatalf("llama-server should have 4 fields, got %+v", got)
	}
	want := []string{"host", "port", "protocol", "api_key"}
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

func TestSpecCloudflareAIGateway(t *testing.T) {
	spec := Spec("cloudflare-ai-gateway")
	if len(spec) != 3 {
		t.Fatalf("spec = %+v", spec)
	}
	token, account, base := spec[0], spec[1], spec[2]
	if token.Name != "token" || !token.Secret || len(token.EnvVars) != 1 || token.EnvVars[0] != "CF_AIG_TOKEN" {
		t.Fatalf("token field = %+v", token)
	}
	if account.Name != "account_id" || account.Secret || len(account.EnvVars) != 2 || account.EnvVars[0] != "CF_ACCOUNT_ID" || account.EnvVars[1] != "CLOUDFLARE_ACCOUNT_ID" {
		t.Fatalf("account_id field = %+v", account)
	}
	if base.Name != "base_url" || base.Secret || !base.Optional || len(base.EnvVars) != 1 || base.EnvVars[0] != "CF_AIG_URL" {
		t.Fatalf("base_url field = %+v", base)
	}
}
