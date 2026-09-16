// Package credentials implements layered credential resolution for model
// providers: environment, managed files, .netrc, and the host keychain.
package credentials

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Source identifies where a credential came from.
type Source string

const (
	SourceEnv         Source = "env"
	SourceProjectFile Source = "project-file"
	SourceUserFile    Source = "user-file"
	SourceNetrc       Source = "netrc"
	SourceKeychain    Source = "keychain"
	SourceNone        Source = "none"
)

// Field is one credential a provider requires.
type Field struct {
	Name    string   // "api_key", "account_id", "gateway_id"
	EnvVars []string // ordered
	Secret  bool     // api_key true; account_id and gateway_id false
}

// Host returns the canonical host name for netrc lookups.
func (f Field) Host(provider string) string {
	return providerHost(provider)
}

// Spec returns the required fields for a provider.
func Spec(provider string) []Field {
	switch provider {
	case "cloudflare-workers-ai":
		return []Field{
			{Name: "api_key", EnvVars: []string{"CLOUDFLARE_API_KEY"}, Secret: true},
			{Name: "account_id", EnvVars: []string{"CLOUDFLARE_ACCOUNT_ID"}, Secret: false},
		}
	case "cloudflare-ai-gateway":
		return []Field{
			{Name: "api_key", EnvVars: []string{"CLOUDFLARE_API_KEY"}, Secret: true},
			{Name: "account_id", EnvVars: []string{"CLOUDFLARE_ACCOUNT_ID"}, Secret: false},
			{Name: "gateway_id", EnvVars: []string{"CLOUDFLARE_GATEWAY_ID"}, Secret: false},
		}
	case "anthropic":
		return []Field{
			{Name: "api_key", EnvVars: []string{"ANTHROPIC_API_KEY"}, Secret: true},
		}
	case "openai":
		return []Field{
			{Name: "api_key", EnvVars: []string{"OPENAI_API_KEY"}, Secret: true},
		}
	case "openrouter":
		return []Field{
			{Name: "api_key", EnvVars: []string{"OPENROUTER_API_KEY"}, Secret: true},
		}
	case "google-gemini":
		return []Field{
			{Name: "api_key", EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, Secret: true},
		}
	case "ollama":
		// Ollama is local and needs no credential; a missing key is not a
		// misconfiguration.
		return nil
	case "github-copilot":
		return []Field{
			{Name: "oauth_token", EnvVars: []string{"GITHUB_COPILOT_TOKEN", "GH_TOKEN"}, Secret: true},
		}
	case "huggingface":
		// Used for gated model downloads and as a chat provider via the
		// Hugging Face OpenAI-compatible Serverless Inference API.
		return []Field{
			{Name: "api_key", EnvVars: []string{"HF_TOKEN", "HUGGINGFACE_TOKEN"}, Secret: true},
		}
	default:
		// An unknown name is a custom provider, never a fallback to OpenAI.
		// The derived variable is the fail-closed default; a profile's
		// api_key_env is prepended by the resolver when one is configured.
		return []Field{
			{Name: "api_key", EnvVars: []string{EnvVarForProvider(provider)}, Secret: true},
		}
	}
}

// EnvVarForProvider returns the conventional environment variable holding a
// custom provider's API key: SIGNET_<UPPER_SNAKE_NAME>_API_KEY.
func EnvVarForProvider(provider string) string {
	return "SIGNET_" + upperSnake(provider) + "_API_KEY"
}

func upperSnake(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Value carries a resolved credential and its provenance.
type Value struct {
	Field    string `json:"field"`
	Location string `json:"location"`
	Source   Source `json:"source"`
	Secret   bool   `json:"secret"`
	value    string
}

// Reveal returns the raw secret value.
func (v Value) Reveal() string { return v.value }

func (v Value) String() string {
	if v.Secret {
		return "<redacted>"
	}
	return v.value
}

func (v Value) GoString() string {
	return fmt.Sprintf("credentials.Value{Field:%q, Location:%q, Source:%q, Secret:%v, value:%q}", v.Field, v.Location, v.Source, v.Secret, v.String())
}

// MarshalJSON renders the value as "<redacted>" when Secret is true.
func (v Value) MarshalJSON() ([]byte, error) {
	type raw Value
	r := struct {
		raw
		Value string `json:"value"`
	}{
		raw: raw(v),
	}
	if v.Secret {
		r.Value = "<redacted>"
	} else {
		r.Value = v.value
	}
	return json.Marshal(r)
}

// Set is the result of resolving all fields for one provider.
type Set struct {
	Provider string           `json:"provider"`
	Values   map[string]Value `json:"values"`
	Missing  []string         `json:"missing"`
	Notes    []string         `json:"notes,omitempty"`
}

// Complete reports whether every required field resolved.
func (s Set) Complete() bool { return len(s.Missing) == 0 }

// Get returns the revealed value for a field, if present.
func (s Set) Get(field string) (string, bool) {
	v, ok := s.Values[field]
	if !ok {
		return "", false
	}
	return v.Reveal(), true
}

// Redact scrubs known secrets out of arbitrary text.
func Redact(text string, secrets []string) string {
	for _, s := range secrets {
		if len(s) < 3 {
			continue
		}
		text = strings.ReplaceAll(text, s, "<redacted>")
	}
	return text
}

func providerHost(provider string) string {
	switch provider {
	case "openai":
		return "api.openai.com"
	case "anthropic":
		return "api.anthropic.com"
	case "cloudflare-workers-ai":
		return "api.cloudflare.com"
	case "cloudflare-ai-gateway":
		return "gateway.ai.cloudflare.com"
	case "openrouter":
		return "openrouter.ai"
	case "google-gemini":
		return "generativelanguage.googleapis.com"
	case "github-copilot":
		return "api.githubcopilot.com"
	case "huggingface":
		return "huggingface.co"
	default:
		return ""
	}
}
