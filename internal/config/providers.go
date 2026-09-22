package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

// envNameRe matches an environment variable name: a letter or underscore,
// then letters, digits, or underscores. This is the shape api_key_env must
// take, and the shape the discovery scanner reuses.
var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ValidEnvName reports whether name is a plausible environment variable name.
func ValidEnvName(name string) bool {
	return envNameRe.MatchString(name)
}

// ValidateProviders rejects invalid custom provider definitions. Fails closed:
// an invalid block disables the whole resolve rather than half-configuring a
// provider with an attacker-controlled or malformed shape.
func ValidateProviders(s Settings) error {
	for name, p := range s.Providers {
		if err := validateProvider(name, p); err != nil {
			return err
		}
	}
	return ValidateProviderLabels(s)
}

func validateProvider(name string, p ProviderProfile) error {
	if provider.Builtin(name) {
		return fmt.Errorf("provider %q collides with a built-in provider", name)
	}
	if !provider.ValidCustomName(name) {
		return fmt.Errorf("invalid provider name %q", name)
	}
	if !validBaseURL(p.BaseURL) {
		return fmt.Errorf("provider %q: invalid base_url %q", name, p.BaseURL)
	}
	if !validSurface(p.API) {
		return fmt.Errorf("provider %q: unknown api %q", name, p.API)
	}
	if p.Auth != "" && !validAuth(p.Auth) {
		return fmt.Errorf("provider %q: unknown auth %q", name, p.Auth)
	}
	if p.APIKeyEnv != "" && !ValidEnvName(p.APIKeyEnv) {
		return fmt.Errorf("provider %q: invalid api_key_env %q", name, p.APIKeyEnv)
	}
	if !validKind(p.Kind) {
		return fmt.Errorf("provider %q: unknown kind %q (want \"ollama\", \"llama-server\", \"openai-compatible\", or empty)", name, p.Kind)
	}
	if p.Protocol != "" && !ValidOllamaProtocol(p.Protocol) {
		return fmt.Errorf("provider %q: invalid protocol %q (want http or https)", name, p.Protocol)
	}
	if p.Port != "" && !ValidOllamaPort(p.Port) {
		return fmt.Errorf("provider %q: invalid port %q", name, p.Port)
	}
	return nil
}

func validKind(kind string) bool {
	switch kind {
	case "", "ollama", "llama-server", "openai-compatible":
		return true
	}
	return false
}

// ValidOllamaPort reports whether s is empty or a valid TCP port.
func ValidOllamaPort(s string) bool {
	if s == "" {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

// ValidOllamaProtocol reports whether s is empty or a valid HTTP scheme.
func ValidOllamaProtocol(s string) bool {
	if s == "" {
		return true
	}
	return s == "http" || s == "https"
}

// ValidateProviderLabels rejects display labels that could forge delimiter
// markup or collide with a provider slug. Fails closed: a bad label
// invalidates the settings file exactly as a malformed profile does.
func ValidateProviderLabels(s Settings) error {
	seen := map[string]string{} // lowercased label -> provider name that owns it
	for name := range s.Providers {
		if provider.Builtin(name) {
			continue
		}
		if err := checkProviderLabel(s, name, seen); err != nil {
			return err
		}
	}
	for _, name := range provider.Names() {
		if err := checkProviderLabel(s, name, seen); err != nil {
			return err
		}
	}
	// The label may also collide with a built-in slug or a configured slug.
	for _, name := range provider.Names() {
		seen[strings.ToLower(name)] = name
	}
	for name := range s.Providers {
		seen[strings.ToLower(name)] = name
	}
	for name, label := range s.ProviderLabels {
		key := strings.ToLower(strings.TrimSpace(label))
		if owner, ok := seen[key]; ok && owner != name {
			return fmt.Errorf("provider label %q for %q collides with provider %q", label, name, owner)
		}
	}
	return nil
}

// checkProviderLabel validates one configured label and records it.
func checkProviderLabel(s Settings, name string, seen map[string]string) error {
	label, ok := s.ProviderLabels[name]
	if !ok {
		return nil
	}
	if !validLabelText(label) {
		return fmt.Errorf("provider label %q for %q is invalid (1–64 printable runes, no <, >, or newlines)", label, name)
	}
	key := strings.ToLower(strings.TrimSpace(label))
	if owner, dup := seen[key]; dup {
		return fmt.Errorf("provider label %q for %q collides with the label of %q", label, name, owner)
	}
	seen[key] = name
	return nil
}

func validLabelText(label string) bool {
	n := 0
	for _, r := range label {
		n++
		if !unicode.IsPrint(r) || unicode.Is(unicode.C, r) {
			return false
		}
		if r == '<' || r == '>' {
			return false
		}
	}
	return n >= 1 && n <= 64
}

func validBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func validSurface(s wire.Surface) bool {
	switch s {
	case wire.SurfaceOpenAIChat, wire.SurfaceOpenAIResponses, wire.SurfaceAnthropicMessages:
		return true
	}
	return false
}

func validAuth(a string) bool {
	switch provider.Auth(a) {
	case provider.AuthBearer, provider.AuthXAPIKey, provider.AuthCFAIG:
		return true
	}
	return false
}
