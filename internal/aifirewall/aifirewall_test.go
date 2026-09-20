package aifirewall

import (
	"strings"
	"testing"
)

func TestSlugMappings(t *testing.T) {
	cases := []struct {
		prov string
		want string
		ok   bool
	}{
		{"anthropic", "anthropic", true},
		{"openai", "openai", true},
		{"openrouter", "openrouter", true},
		{"ollama", "", false},
		{"llama-server", "", false},
		{"github-copilot", "", false},
	}
	for _, tc := range cases {
		got, ok := Slug(tc.prov)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Slug(%q) = (%q, %v), want (%q, %v)", tc.prov, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBaseURL(t *testing.T) {
	cases := []struct {
		gateway, slug, org, want string
	}{
		{"https://guardrails.vulnetix.com", "anthropic", "org-1", "https://guardrails.vulnetix.com/anthropic/org-1"},
		{"https://guardrails.vulnetix.com", "openai", "org-1", "https://guardrails.vulnetix.com/openai/org-1/v1"},
		{"https://gw.example.com/", "openrouter", "org-2", "https://gw.example.com/openrouter/org-2/v1"},
	}
	for _, tc := range cases {
		got := BaseURL(tc.gateway, tc.slug, tc.org)
		if got != tc.want {
			t.Errorf("BaseURL(%q,%q,%q) = %q, want %q", tc.gateway, tc.slug, tc.org, got, tc.want)
		}
	}
}

func TestIsGatewayURL(t *testing.T) {
	if !IsGatewayURL("", "https://guardrails.vulnetix.com/anthropic/org-1") {
		t.Error("expected default gateway URL to be detected")
	}
	if IsGatewayURL("", "https://api.openai.com/v1") {
		t.Error("expected OpenAI URL not to be detected as gateway")
	}
	if !IsGatewayURL("https://gw.example.com", "https://gw.example.com/openai/org-1/v1") {
		t.Error("expected self-hosted gateway URL to be detected")
	}
}

func TestURLPathUUID(t *testing.T) {
	if got := URLPathUUID("https://guardrails.vulnetix.com/anthropic/org-1"); got != "org-1" {
		t.Errorf("URLPathUUID = %q, want org-1", got)
	}
	if got := URLPathUUID("https://api.openai.com/v1"); got != "" {
		t.Errorf("URLPathUUID = %q, want empty", got)
	}
}

func TestHostOf(t *testing.T) {
	if got := HostOf("https://guardrails.vulnetix.com/anthropic/org-1"); got != "guardrails.vulnetix.com" {
		t.Errorf("HostOf = %q, want guardrails.vulnetix.com", got)
	}
}

func TestNonRoutableProviders(t *testing.T) {
	for _, p := range []string{"ollama", "llama-server", "github-copilot", "cloudflare-workers-ai", "google-gemini", "huggingface"} {
		if _, ok := Slug(p); ok {
			t.Errorf("Slug(%q) unexpectedly returned ok=true", p)
		}
	}
}

func TestBaseURLTrimsTrailingSlash(t *testing.T) {
	got := BaseURL("https://gw.example.com/", "openai", "org")
	if strings.HasSuffix(got, "//") {
		t.Errorf("BaseURL has double slash: %s", got)
	}
}
