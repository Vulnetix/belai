package aifirewall

import (
	"sort"
	"testing"
)

func TestProvidersSortedAndMatchesSlugs(t *testing.T) {
	got := Providers()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Providers() not sorted: %v", got)
	}
	if len(got) != len(slugs) {
		t.Fatalf("len = %d, want %d", len(got), len(slugs))
	}
	want := make([]string, 0, len(slugs))
	for name := range slugs {
		want = append(want, name)
	}
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Providers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSlug(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"anthropic", "anthropic", true},
		{"Anthropic", "anthropic", true}, // case-insensitive
		{"OPENAI", "openai", true},
		{"openrouter", "openrouter", true},
		{"deepseek", "deepseek", true},
		{"xai", "xai", true},
		{"together", "together", true},
		{"fireworks", "fireworks", true},
		{"alibaba", "alibaba", true},
		{"moonshot", "moonshot", true},
		{"minimax", "minimax", true},
		{"mistral", "mistral", true},
		{"groq", "groq", true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := Slug(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Slug(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
		slug    string
		org     string
		want    string
	}{
		{"anthropic no trailing slash", "https://guardrails.vulnetix.com", "anthropic", "org-1",
			"https://guardrails.vulnetix.com/anthropic/org-1"},
		{"anthropic trailing slash", "https://guardrails.vulnetix.com/", "anthropic", "org-1",
			"https://guardrails.vulnetix.com/anthropic/org-1"},
		{"openai", "https://guardrails.vulnetix.com", "openai", "org-1",
			"https://guardrails.vulnetix.com/openai/org-1/v1"},
		{"openai trailing slash", "https://guardrails.vulnetix.com/", "openai", "org-1",
			"https://guardrails.vulnetix.com/openai/org-1/v1"},
		{"deepseek", "https://gw.example.com", "deepseek", "abc", "https://gw.example.com/deepseek/abc/v1"},
	}
	for _, tc := range cases {
		if got := BaseURL(tc.gateway, tc.slug, tc.org); got != tc.want {
			t.Errorf("BaseURL(%q, %q, %q) = %q, want %q", tc.gateway, tc.slug, tc.org, got, tc.want)
		}
	}
}

func TestIsGatewayURL(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
		baseURL string
		want    bool
	}{
		{"explicit match", "https://guardrails.vulnetix.com", "https://guardrails.vulnetix.com/openai/org/v1", true},
		{"explicit host match different path", "https://guardrails.vulnetix.com", "https://guardrails.vulnetix.com/whatever", true},
		{"explicit mismatch", "https://gw.example.com", "https://guardrails.vulnetix.com/openai/org/v1", false},
		{"default match", "", "https://guardrails.vulnetix.com/openai/org/v1", true},
		{"default mismatch", "", "https://api.openai.com/v1", false},
		{"explicit self-hosted match", "https://gw.example.com", "https://gw.example.com/anthropic/org", true},
	}
	for _, tc := range cases {
		if got := IsGatewayURL(tc.gateway, tc.baseURL); got != tc.want {
			t.Errorf("IsGatewayURL(%q, %q) = %v, want %v", tc.gateway, tc.baseURL, got, tc.want)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://guardrails.vulnetix.com/openai/org/v1", "guardrails.vulnetix.com"},
		{"http://api.openai.com", "api.openai.com"},
		{"not a url", ""},
		{"", ""},
		{"://bad", ""},
	}
	for _, tc := range cases {
		if got := HostOf(tc.in); got != tc.want {
			t.Errorf("HostOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestURLPathUUID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://guardrails.vulnetix.com/openai/org-123/v1", "org-123"},
		{"https://guardrails.vulnetix.com/anthropic/org-456", "org-456"},
		{"https://guardrails.vulnetix.com/openai/org-789/v1/chat/completions", "org-789"},
		{"https://guardrails.vulnetix.com/anthropic", ""}, // only one path segment
		{"https://guardrails.vulnetix.com", ""},
		{"://bad", ""},
	}
	for _, tc := range cases {
		if got := URLPathUUID(tc.in); got != tc.want {
			t.Errorf("URLPathUUID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
