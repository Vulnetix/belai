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

// Save writes a profile and returns its path. It rejects names that would
// overwrite a built-in profile once sanitised.
func Save(p AgentProfile) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if !p.Builtin && collidesWithBuiltin(p) {
		return "", fmt.Errorf("profile name %q collides with a built-in profile", p.Name)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, p.FileName())
	// A rename must never clobber a sibling profile: refuse to write a path
	// that already exists and holds a different Name.
	if existing, err := os.ReadFile(path); err == nil {
		var prev AgentProfile
		if json.Unmarshal(existing, &prev) == nil && prev.Name != "" && prev.Name != p.Name {
			return "", fmt.Errorf("refusing to overwrite %s: it holds profile %q", p.FileName(), prev.Name)
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// SaveMoving saves p and removes oldFile when it names a different on-disk
// file. A removal failure is reported rather than swallowed, so a rename that
// writes the new file but cannot drop the old one is visible.
func SaveMoving(p AgentProfile, oldFile string) (string, error) {
	path, err := Save(p)
	if err != nil {
		return "", err
	}
	if oldFile != "" && oldFile != p.FileName() {
		dir, derr := Dir()
		if derr != nil {
			return path, derr
		}
		if rerr := os.Remove(filepath.Join(dir, oldFile)); rerr != nil {
			return path, rerr
		}
	}
	return path, nil
}

// Load reads a profile by name. Built-in names resolve from the embedded set
// and never touch disk, so a user file named signet_triage-vulns.json cannot
// shadow signet:triage-vulns.
func Load(name string) (AgentProfile, error) {
	if p, ok := extraProfile(name); ok {
		return p, nil
	}
	if IsBuiltin(name) {
		p, ok := builtinProfiles[name]
		if !ok {
			return AgentProfile{}, fmt.Errorf("unknown built-in profile %q", name)
		}
		return p, nil
	}
	dir, err := Dir()
	if err != nil {
		return AgentProfile{}, err
	}
	p := AgentProfile{Name: name}
	fileName := p.FileName()
	path := filepath.Join(dir, fileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return AgentProfile{}, err
		}
		// The profile may have been renamed on disk. Scan for a file whose
		// embedded Name matches, so /agent start <name> and the picker keep
		// resolving after a file-name edit.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return AgentProfile{}, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
			if readErr != nil {
				continue
			}
			var cand AgentProfile
			if json.Unmarshal(data, &cand) != nil {
				continue
			}
			if cand.Name != name {
				continue
			}
			cand.File = e.Name()
			if vErr := cand.Validate(); vErr != nil {
				return AgentProfile{}, fmt.Errorf("invalid profile %s: %w", name, vErr)
			}
			return cand, nil
		}
		return AgentProfile{}, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return AgentProfile{}, err
	}
	p.File = fileName
	if err := p.Validate(); err != nil {
		return AgentProfile{}, fmt.Errorf("invalid profile %s: %w", name, err)
	}
	return p, nil
}

// List returns all stored agent profiles plus built-ins, sorted by name.
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
		// Load sets File from the entry name; pin it again here so the record
		// survives any future Load fast-path change.
		p.File = e.Name()
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// Plugin profiles follow the user's, named "plugin:name" so they can
	// never shadow a user or built-in profile.
	if ExtraProfiles != nil {
		out = append(out, ExtraProfiles()...)
	}
	// Append built-ins after user profiles so they are selectable but cannot be
	// clobbered on disk.
	for _, n := range builtinNames() {
		out = append(out, builtinProfiles[n])
	}
	return out, nil
}

// Delete removes a profile by name. Built-in profiles cannot be deleted.
func Delete(name string) error {
	if IsBuiltin(name) {
		return fmt.Errorf("cannot delete built-in profile %q", name)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	p := AgentProfile{Name: name}
	return os.Remove(filepath.Join(dir, p.FileName()))
}

// collidesWithBuiltin reports whether the sanitised filename of p matches a
// built-in's sanitised filename.
func collidesWithBuiltin(p AgentProfile) bool {
	return collidesWithBuiltinFileName(p)
}

// ExtraProfiles returns profiles from enabled plugins, each already
// validated and named "plugin:name". nil means none. Set once at startup.
var ExtraProfiles func() []AgentProfile

// extraProfile finds a plugin profile by exact name. Only a namespaced name
// (with a colon, outside the built-in prefix) can match.
func extraProfile(name string) (AgentProfile, bool) {
	if ExtraProfiles == nil || IsBuiltin(name) || !strings.Contains(name, ":") {
		return AgentProfile{}, false
	}
	for _, p := range ExtraProfiles() {
		if p.Name == name {
			return p, true
		}
	}
	return AgentProfile{}, false
}
