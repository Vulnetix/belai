package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/config"
)

// Dir returns the agent-profiles directory (~/.signet/profiles/agents).
func Dir() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "profiles", "agents"), nil
}

// Save writes a profile and returns its path.
func Save(p AgentProfile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, p.FileName())
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a profile by name.
func Load(name string) (AgentProfile, error) {
	p := AgentProfile{Name: name}
	dir, err := Dir()
	if err != nil {
		return AgentProfile{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, p.FileName()))
	if err != nil {
		return AgentProfile{}, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return AgentProfile{}, err
	}
	if err := p.Validate(); err != nil {
		return AgentProfile{}, fmt.Errorf("invalid profile %s: %w", name, err)
	}
	return p, nil
}

// List returns all stored agent profiles, sorted by name.
func List() ([]AgentProfile, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []AgentProfile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		p, err := Load(name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Delete removes a profile by name.
func Delete(name string) error {
	p := AgentProfile{Name: name}
	dir, err := Dir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, p.FileName()))
}
