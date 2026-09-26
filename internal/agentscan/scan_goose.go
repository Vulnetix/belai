package agentscan

import (
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// gooseMap maps a Goose secret name onto a Belai provider/field.
func gooseMap(name string) (provider, field string, ok bool) {
	switch name {
	case "OPENAI_API_KEY":
		return "openai", "api_key", true
	case "ANTHROPIC_API_KEY":
		return "anthropic", "api_key", true
	case "GEMINI_API_KEY", "GOOGLE_API_KEY":
		return "google-gemini", "api_key", true
	case "OPENROUTER_API_KEY":
		return "openrouter", "api_key", true
	case "GITHUB_COPILOT_TOKEN", "GH_TOKEN":
		return "github-copilot", "oauth_token", true
	}
	return "", "", false
}

func scanGoose(home string) []Found {
	path := filepath.Join(configDir(home), "goose", "secrets.yaml")
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return []Found{note("goose", path, "unparseable secrets.yaml")}
	}
	var out []Found
	for name, val := range m {
		if val == "" {
			continue
		}
		prov, field, ok := gooseMap(name)
		if !ok {
			out = append(out, note("goose", path, name+" is not a mapped Belai credential"))
			continue
		}
		out = append(out, Found{Agent: "goose", Provider: prov, Field: field, Location: path, value: val})
	}
	sortFounds(out)
	return out
}
