package agentscan

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/wire"
)

func scanPi(home string) []Found {
	dir := filepath.Join(home, ".pi", "agent")
	var out []Found
	out = append(out, scanPiModels(filepath.Join(dir, "models.json"), filepath.Join(dir, "models-store.json"))...)
	out = append(out, scanPiAuth(filepath.Join(dir, "auth.json"))...)
	return out
}

// mapPiAPI maps Pi's api vocabulary onto a wire surface. Pi uses
// "openai-completions" where Belai uses "openai-chat"; the other two values
// pass through unchanged. An unrecognised value is not importable.
func mapPiAPI(api string) (wire.Surface, bool) {
	switch api {
	case "openai-completions":
		return wire.SurfaceOpenAIChat, true
	case "openai-responses":
		return wire.SurfaceOpenAIResponses, true
	case "anthropic-messages":
		return wire.SurfaceAnthropicMessages, true
	default:
		return "", false
	}
}

type piModelsFile struct {
	Providers map[string]struct {
		API     string         `json:"api"`
		APIKey  string         `json:"apiKey"`
		BaseURL string         `json:"baseUrl"`
		Compat  map[string]any `json:"compat"`
	} `json:"providers"`
}

type piModelsStoreFile struct {
	Providers map[string]struct {
		Models []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"contextWindow"`
			MaxTokens     int    `json:"maxTokens"`
		} `json:"models"`
	} `json:"providers"`
}

func scanPiModels(modelsPath, storePath string) []Found {
	// The models-store.json is a cached catalogue keyed by provider; it carries
	// no secrets. Attach each provider's models to its profile.
	storeModels := map[string][]config.ProviderModel{}
	if data, err := readCapped(storePath); err == nil {
		var sf piModelsStoreFile
		if json.Unmarshal(data, &sf) == nil {
			for name, p := range sf.Providers {
				models := make([]config.ProviderModel, 0, len(p.Models))
				for _, m := range p.Models {
					models = append(models, config.ProviderModel{
						ID:            m.ID,
						Name:          m.ID,
						ContextWindow: m.ContextWindow,
						MaxTokens:     m.MaxTokens,
					})
				}
				storeModels[name] = models
			}
		}
	}

	data, err := readCapped(modelsPath)
	if err != nil {
		return nil
	}
	var mf piModelsFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return []Found{note("pi", modelsPath, "unparseable models.json")}
	}

	var out []Found
	for name, p := range mf.Providers {
		if provider.Builtin(name) {
			out = append(out, note("pi", modelsPath, name+" collides with a built-in provider"))
			continue
		}
		if !provider.ValidCustomName(name) {
			out = append(out, note("pi", modelsPath, "invalid provider name "+name))
			continue
		}
		surface, ok := mapPiAPI(p.API)
		if !ok {
			out = append(out, note("pi", modelsPath, "unknown api dialect "+p.API))
			continue
		}
		f := Found{
			Agent:    "pi",
			Provider: name,
			Field:    "api_key",
			Location: modelsPath,
			Profile: &config.ProviderProfile{
				BaseURL: p.BaseURL,
				API:     surface,
				Models:  storeModels[name],
			},
			value: p.APIKey,
		}
		if len(p.Compat) > 0 {
			f.Note = "compat flags dropped: " + strings.Join(compatKeys(p.Compat), ", ")
		}
		out = append(out, f)
	}
	sortFounds(out)
	return out
}

func scanPiAuth(path string) []Found {
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		return []Found{note("pi", path, "unparseable auth.json")}
	}
	var out []Found
	for name, key := range auth {
		if key == "" {
			continue
		}
		if provider.Builtin(name) {
			out = append(out, Found{Agent: "pi", Provider: name, Field: "api_key", Location: path, value: key})
		} else {
			out = append(out, note("pi", path, name+" is not a known provider"))
		}
	}
	sortFounds(out)
	return out
}

func compatKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
