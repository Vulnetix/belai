package agentscan

import (
	"encoding/json"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/wire"
)

func scanCodex(home string) []Found {
	var out []Found
	out = append(out, scanCodexAuth(filepath.Join(home, ".codex", "auth.json"))...)
	out = append(out, scanCodexConfig(filepath.Join(home, ".codex", "config.toml"))...)
	return out
}

func scanCodexAuth(path string) []Found {
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var auth struct {
		OpenAIAPIKey any    `json:"OPENAI_API_KEY"`
		AuthMode     string `json:"auth_mode"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return []Found{note("codex", path, "unparseable auth.json")}
	}
	if auth.AuthMode == "chatgpt" {
		return []Found{note("codex", path, "auth_mode: chatgpt (OAuth, not an API key)")}
	}
	if key, ok := auth.OpenAIAPIKey.(string); ok && key != "" {
		return []Found{{Agent: "codex", Provider: "openai", Field: "api_key", Location: path, value: key}}
	}
	return nil
}

func scanCodexConfig(path string) []Found {
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		ModelProviders map[string]struct {
			BaseURL string `toml:"base_url"`
			EnvKey  string `toml:"env_key"`
			WireAPI string `toml:"wire_api"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return []Found{note("codex", path, "unparseable config.toml")}
	}
	var out []Found
	for name, p := range cfg.ModelProviders {
		if provider.Builtin(name) {
			out = append(out, note("codex", path, name+" collides with a built-in provider"))
			continue
		}
		if !provider.ValidCustomName(name) {
			out = append(out, note("codex", path, "invalid provider name "+name))
			continue
		}
		surface, ok := mapCodexWireAPI(p.WireAPI)
		if !ok {
			out = append(out, note("codex", path, "unknown wire_api "+p.WireAPI))
			continue
		}
		f := Found{
			Agent:    "codex",
			Provider: name,
			Field:    "api_key",
			Location: path,
			EnvKey:   p.EnvKey,
			Profile: &config.ProviderProfile{
				BaseURL:   p.BaseURL,
				API:       surface,
				APIKeyEnv: p.EnvKey,
			},
		}
		out = append(out, f)
	}
	sortFounds(out)
	return out
}

func mapCodexWireAPI(api string) (wire.Surface, bool) {
	switch api {
	case "chat":
		return wire.SurfaceOpenAIChat, true
	case "responses":
		return wire.SurfaceOpenAIResponses, true
	case "messages":
		return wire.SurfaceAnthropicMessages, true
	default:
		return "", false
	}
}
