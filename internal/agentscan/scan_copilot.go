package agentscan

import (
	"encoding/json"
	"path/filepath"
)

func scanCopilot(home string) []Found {
	path := filepath.Join(home, ".copilot", "config.json")
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var cfg map[string]struct {
		OAuthToken string `json:"oauth_token"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return []Found{note("copilot", path, "unparseable config.json")}
	}
	var out []Found
	for _, e := range cfg {
		if e.OAuthToken != "" {
			out = append(out, Found{
				Agent:    "copilot",
				Provider: "github-copilot",
				Field:    "oauth_token",
				Location: path,
				value:    e.OAuthToken,
			})
		}
	}
	return out
}
