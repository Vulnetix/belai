// Package credentials implements layered credential resolution for model
// providers: environment, managed files, .netrc, and the host keychain.
package credentials

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/provider"
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
	Name     string   // "api_key", "account_id", "gateway_id"
	EnvVars  []string // ordered
	Secret   bool     // api_key true; account_id and gateway_id false
	Optional bool     // when true, absence does not make the provider unconfigured
}

// Host returns the canonical host name for netrc lookups.
func (f Field) Host(provider string) string {
	return providerHost(provider)
}

// convertField maps a provider descriptor field into a credentials.Field.
func convertField(f provider.Field) Field {
	return Field{
		Name:     f.Name,
		EnvVars:  f.EnvVars,
		Secret:   f.Secret,
		Optional: f.Optional,
	}
}

// Spec returns the required fields for a provider.
func Spec(providerName string) []Field {
	return SpecFor(providerName, nil)
}

// SpecFor returns the required fields for a provider, honouring a custom
// profile's kind. With a real kind ("ollama" or "llama-server"), the api_key
// is the template's optional field, so a local instance offers an optional key
// rather than a mandatory one; host/port/protocol stay in the settings profile
// and never become credentials for a custom instance. With no profile (or a
// generic "openai-compatible" profile), Spec's behaviour is preserved verbatim.
func SpecFor(providerName string, prof *config.ProviderProfile) []Field {
	if prof != nil && prof.Kind != "" && prof.Kind != "openai-compatible" {
		if d, ok := provider.Template(prof.Kind); ok {
			optional := false
			for _, f := range d.Fields {
				if f.Name == "api_key" && f.Optional {
					optional = true
				}
			}
			return []Field{
				{Name: "api_key", EnvVars: []string{EnvVarForProvider(providerName)}, Secret: true, Optional: optional},
			}
		}
	}
	if d, ok := provider.Lookup(providerName); ok {
		out := make([]Field, len(d.Fields))
		for i, f := range d.Fields {
			out[i] = convertField(f)
		}
		return out
	}
	// An unknown name is a custom provider, never a fallback to OpenAI.
	// The derived variable is the fail-closed default; a profile's
	// api_key_env is prepended by the resolver when one is configured.
	return []Field{
		{Name: "api_key", EnvVars: []string{EnvVarForProvider(providerName)}, Secret: true},
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

func providerHost(providerName string) string {
	if d, ok := provider.Lookup(providerName); ok {
		return d.NetrcHost
	}
	return ""
}
