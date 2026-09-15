package agentscan

import (
	"encoding/json"
	"path/filepath"
)

func scanClaudeCode(home string) []Found {
	dir := filepath.Join(home, ".claude")
	var out []Found
	out = append(out, scanClaudeSettings(filepath.Join(dir, "settings.json"))...)
	out = append(out, scanClaudeCredentials(filepath.Join(dir, ".credentials.json"))...)
	return out
}

func scanClaudeSettings(path string) []Found {
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var s struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return []Found{note("claude", path, "unparseable settings.json")}
	}
	if v := s.Env["ANTHROPIC_API_KEY"]; v != "" {
		return []Found{{Agent: "claude", Provider: "anthropic", Field: "api_key", Location: path, value: v}}
	}
	if v := s.Env["ANTHROPIC_AUTH_TOKEN"]; v != "" {
		return []Found{{Agent: "claude", Provider: "anthropic", Field: "api_key", Location: path, value: v}}
	}
	return nil
}

func scanClaudeCredentials(path string) []Found {
	data, err := readCapped(path)
	if err != nil {
		return nil
	}
	var c struct {
		ClaudeAiOauth string `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	if c.ClaudeAiOauth != "" {
		return []Found{note("claude", path, "OAuth login, not an API key")}
	}
	return nil
}
