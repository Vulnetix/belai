package agentscan

import (
	"encoding/json"
	"path/filepath"

	"github.com/vulnetix/signet/internal/provider"
)

func scanOpenCode(home string) []Found {
	path := filepath.Join(dataDir(home), "opencode", "auth.json")
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var m map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return []Found{note("opencode", path, "unparseable auth.json")}
	}
	var out []Found
	for name, e := range m {
		if e.Type != "api" || e.Key == "" {
			continue
		}
		if provider.Builtin(name) {
			out = append(out, Found{Agent: "opencode", Provider: name, Field: "api_key", Location: path, value: e.Key})
		} else {
			out = append(out, note("opencode", path, name+" is not a known provider"))
		}
	}
	sortFounds(out)
	return out
}
