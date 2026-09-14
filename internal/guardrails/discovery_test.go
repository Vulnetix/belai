package guardrails

import (
	"reflect"
	"testing"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestBuildBaseURL(t *testing.T) {
	cases := []struct {
		prov Provider
		org  string
		want string
	}{
		{ProviderOpenAI, "acme", "https://guardrails.vulnetix.com/openai/acme/v1"},
		{ProviderAnthropic, "acme", "https://guardrails.vulnetix.com/anthropic/acme"},
	}
	for _, tc := range cases {
		if got := BuildBaseURL(tc.prov, tc.org); got != tc.want {
			t.Fatalf("BuildBaseURL(%q, %q) = %q, want %q", tc.prov, tc.org, got, tc.want)
		}
	}
}

func TestDiscoverAnthropicNoV1(t *testing.T) {
	cfg, err := Discover(envMap(map[string]string{
		"VULNETIX_API_KEY":           "vk-123",
		"VULNETIX_ORG":               "acme",
		"SIGNET_GUARDRAILS_PROVIDER": "anthropic",
	}))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.BaseURL != "https://guardrails.vulnetix.com/anthropic/acme" {
		t.Fatalf("BaseURL = %q (should have no /v1)", cfg.BaseURL)
	}
	if cfg.KeySource != "VULNETIX_API_KEY" {
		t.Fatalf("KeySource = %q", cfg.KeySource)
	}
	if cfg.APIKey != "vk-123" {
		t.Fatalf("APIKey = %q", cfg.APIKey)
	}
}

func TestDiscoverOpenAIDefault(t *testing.T) {
	cfg, err := Discover(envMap(map[string]string{
		"VULNETIX_API_KEY": "vk-123",
		"VULNETIX_ORG":     "acme",
	}))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cfg.Provider != ProviderOpenAI {
		t.Fatalf("Provider = %q, want openai", cfg.Provider)
	}
	if cfg.BaseURL != "https://guardrails.vulnetix.com/openai/acme/v1" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestDiscoverErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"missing key", map[string]string{"VULNETIX_ORG": "acme"}},
		{"missing org", map[string]string{"VULNETIX_API_KEY": "vk"}},
		{"bad provider", map[string]string{"VULNETIX_API_KEY": "vk", "VULNETIX_ORG": "acme", "SIGNET_GUARDRAILS_PROVIDER": "gemini"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Discover(envMap(tc.env)); err == nil {
				t.Fatalf("expected discovery error")
			}
		})
	}
}

func TestConfigureWritesEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	e, err := Configure(envMap(map[string]string{
		"VULNETIX_API_KEY": "vk-123",
		"VULNETIX_ORG":     "acme",
	}))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	want := Entry{
		Provider:  ProviderOpenAI,
		BaseURL:   "https://guardrails.vulnetix.com/openai/acme/v1",
		KeySource: "VULNETIX_API_KEY",
	}
	if !reflect.DeepEqual(want, e) {
		t.Fatalf("Configure = %+v, want %+v", e, want)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
}
