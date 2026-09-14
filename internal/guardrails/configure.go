package guardrails

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vulnetix/signet/internal/config"
)

// Entry is the persisted harness provider entry: base URL + key source only,
// with no custom headers (the ai-firewall surface contract is base_url +
// api_key).
type Entry struct {
	Provider  Provider `json:"provider"`
	BaseURL   string   `json:"base_url"`
	KeySource string   `json:"key_source"`
}

// Configure discovers the guardrails provider and writes the harness provider
// entry to ~/.signet/provider.json.
func Configure(env func(string) string) (Entry, error) {
	cfg, err := Discover(env)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Provider: cfg.Provider, BaseURL: cfg.BaseURL, KeySource: cfg.KeySource}
	if err := Save(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Save writes an entry to ~/.signet/provider.json.
func Save(e Entry) error {
	dir, err := config.GlobalDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "provider.json"), data, 0o600)
}

// Load reads the persisted provider entry.
func Load() (Entry, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return Entry{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "provider.json"))
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return Entry{}, err
	}
	return e, nil
}
