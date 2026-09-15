package config

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

// envNameRe matches an environment variable name: a letter or underscore,
// then letters, digits, or underscores. This is the shape api_key_env must
// take, and the shape the discovery scanner reuses.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	return nil
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
	return nil
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
