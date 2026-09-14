// Package guardrails auto-discovers the Vulnetix ai-firewall / ai-guardrails
// configuration and writes the harness provider entry (base URL + key source)
// with no custom headers, matching the ai-firewall surface contract.
package guardrails

import (
	"fmt"
	"strings"
)

// Provider identifies which ai-firewall relayed surface to use.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

const host = "guardrails.vulnetix.com"

// Config is a discovered guardrails provider configuration.
type Config struct {
	Provider  Provider
	Org       string
	BaseURL   string
	KeySource string
	APIKey    string
}

// Discover resolves the guardrails provider config from environment variables.
// env is an environment lookup (os.Getenv in production; a map in tests).
func Discover(env func(string) string) (Config, error) {
	key := env("VULNETIX_API_KEY")
	if key == "" {
		return Config{}, fmt.Errorf("VULNETIX_API_KEY not set")
	}
	org := strings.Trim(env("VULNETIX_ORG"), "/")
	if org == "" {
		return Config{}, fmt.Errorf("VULNETIX_ORG not set")
	}

	prov := Provider(strings.ToLower(strings.TrimSpace(env("SIGNET_GUARDRAILS_PROVIDER"))))
	if prov == "" {
		prov = Provider(strings.ToLower(strings.TrimSpace(env("VULNETIX_PROVIDER"))))
	}
	if prov == "" {
		prov = ProviderOpenAI
	}
	if prov != ProviderOpenAI && prov != ProviderAnthropic {
		return Config{}, fmt.Errorf("unsupported guardrails provider %q", prov)
	}

	return Config{
		Provider:  prov,
		Org:       org,
		BaseURL:   BuildBaseURL(prov, org),
		KeySource: "VULNETIX_API_KEY",
		APIKey:    key,
	}, nil
}

// BuildBaseURL returns the ai-firewall base URL for a provider and org.
// OpenAI-style base URLs carry /v1; the Anthropic base URL has no /v1.
func BuildBaseURL(p Provider, org string) string {
	org = strings.Trim(org, "/")
	switch p {
	case ProviderAnthropic:
		return "https://" + host + "/anthropic/" + org
	default:
		return "https://" + host + "/openai/" + org + "/v1"
	}
}
