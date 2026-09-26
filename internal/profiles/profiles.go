// Package profiles manages agent profiles stored under ~/.belai/profiles/.
// Profiles are selectable at startup and mid-session via a /profile command.
package profiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Profile is a named agent profile. Its content is carried into the system
// prompt when the profile is active.
type Profile struct {
	Name    string   `json:"name"`
	Content string   `json:"content"`
	Tools   []string `json:"tools,omitempty"` // empty means every registered tool
	Builtin bool     `json:"-"`               // true for harness-supplied profiles
}

const BuiltinPrefix = "belai:"

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func nameFile(name string) (string, error) {
	clean := strings.Trim(unsafeName.ReplaceAllString(name, "_"), "._-")
	if clean == "" {
		return "", fmt.Errorf("invalid profile name %q", name)
	}
	return clean + ".json", nil
}

// IsBuiltin reports whether name is a harness-supplied profile name.
func IsBuiltin(name string) bool {
	return strings.HasPrefix(name, BuiltinPrefix)
}

// Dir returns the profiles directory (~/.belai/profiles).
func Dir() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "profiles"), nil
}

// Validate checks a profile's required fields.
func Validate(p Profile) error {
	return validate(p, false)
}

func validate(p Profile, allowBuiltin bool) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("profile name is required")
	}
	if strings.TrimSpace(p.Content) == "" {
		return errors.New("profile content is required")
	}
	if !allowBuiltin && IsBuiltin(p.Name) {
		return fmt.Errorf("profile name %q is reserved for built-in profiles", p.Name)
	}
	if _, err := nameFile(p.Name); err != nil {
		return err
	}
	return nil
}

// Save writes a profile and returns its path.
func Save(p Profile) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fn, _ := nameFile(p.Name)
	path := filepath.Join(dir, fn)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a profile by name. Built-in names resolve from the embedded
// profile set and never touch disk, so a user file named belai_debug.json
// cannot shadow the built-in belai:debug profile.
func Load(name string) (Profile, error) {
	if IsBuiltin(name) {
		p, ok := builtinByName(name)
		if !ok {
			return Profile{}, fmt.Errorf("unknown built-in profile %q", name)
		}
		return p, nil
	}
	fn, err := nameFile(name)
	if err != nil {
		return Profile{}, err
	}
	dir, err := Dir()
	if err != nil {
		return Profile{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, fn))
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, err
	}
	if err := Validate(p); err != nil {
		return Profile{}, fmt.Errorf("invalid profile %s: %w", name, err)
	}
	return p, nil
}

// Switch selects a profile by name (mid-session switch).
func Switch(name string) (Profile, error) {
	return Load(name)
}

// List returns all stored profiles, sorted by name. Built-in profiles are
// appended after user profiles so they are selectable but cannot be clobbered
// on disk.
func List() ([]Profile, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []Profile
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
	out = append(out, builtins()...)
	return out, nil
}
