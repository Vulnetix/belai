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
		"cloudflare-ai-gateway": {"api_key", "account_id", "gateway_id"},
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
