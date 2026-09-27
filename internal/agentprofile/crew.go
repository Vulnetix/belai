package agentprofile

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// A crew is a named set of worker profiles started together — the delivery
// crew's scout, builders and reviewer. Crews hold no settings of their own:
// every member is an ordinary profile, validated on its own, and a crew only
// says which ones run and how many of each.

// Crew is a named set of worker profiles.
type Crew struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []Member `json:"members"`
	// Builtin is true for embedded crews; never persisted.
	Builtin bool `json:"-"`
}

// Member is one profile in a crew and how many workers run it.
type Member struct {
	Profile  string `json:"profile"`
	Replicas int    `json:"replicas,omitempty"`
}

// Crew limits.
const (
	MaxCrewMembers = 8
	MaxReplicas    = 8
)

//go:embed crews/*.json
var crewFS embed.FS

// Count is the member's worker count, defaulting to one.
func (m Member) Count() int {
	if m.Replicas <= 0 {
		return 1
	}
	return m.Replicas
}

// Validate checks the crew's shape and that every member is a worker profile.
func (c Crew) Validate() error {
	if strings.TrimSpace(c.Name) == "" || strings.Trim(unsafeName.ReplaceAllString(c.Name, "_"), ".-_") == "" {
		return fmt.Errorf("invalid crew name %q", c.Name)
	}
	if !c.Builtin && IsBuiltin(c.Name) {
		return fmt.Errorf("crew name %q is reserved for built-in crews", c.Name)
	}
	if len(c.Members) == 0 || len(c.Members) > MaxCrewMembers {
		return fmt.Errorf("a crew has 1 to %d members", MaxCrewMembers)
	}
	for _, m := range c.Members {
		if m.Replicas < 0 || m.Replicas > MaxReplicas {
			return fmt.Errorf("member %s: replicas run 1 to %d", m.Profile, MaxReplicas)
		}
		p, err := Load(m.Profile)
		if err != nil {
			return fmt.Errorf("member %s: %w", m.Profile, err)
		}
		if p.Mode != ModeWorker {
			return fmt.Errorf("member %s is not a worker profile (mode %s)", m.Profile, p.Mode)
		}
	}
	return nil
}

// Workers is the total worker count.
func (c Crew) Workers() int {
	n := 0
	for _, m := range c.Members {
		n += m.Count()
	}
	return n
}

// CrewsDir returns the user's crews directory.
func CrewsDir() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "profiles", "crews"), nil
}

func builtinCrews() map[string]Crew {
	out := map[string]Crew{}
	files, err := crewFS.ReadDir("crews")
	if err != nil {
		return out
	}
	for _, f := range files {
		data, err := crewFS.ReadFile("crews/" + f.Name())
		if err != nil {
			continue
		}
		var c Crew
		if json.Unmarshal(data, &c) != nil {
			continue
		}
		c.Builtin = true
		out[c.Name] = c
	}
	return out
}

// LoadCrew reads a crew by name: built-ins first, then the user's directory.
func LoadCrew(name string) (Crew, error) {
	if c, ok := builtinCrews()[name]; ok {
		return c, c.Validate()
	}
	dir, err := CrewsDir()
	if err != nil {
		return Crew{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, deriveFileName(name)))
	if errors.Is(err, os.ErrNotExist) {
		return Crew{}, fmt.Errorf("no crew named %q", name)
	}
	if err != nil {
		return Crew{}, err
	}
	var c Crew
	if err := json.Unmarshal(data, &c); err != nil {
		return Crew{}, fmt.Errorf("crew %s: %w", name, err)
	}
	if c.Name != name {
		return Crew{}, fmt.Errorf("crew file %s holds crew %q", deriveFileName(name), c.Name)
	}
	return c, c.Validate()
}

// ListCrews returns every crew: the user's, then the built-ins. A user crew
// that fails to parse is skipped.
func ListCrews() []Crew {
	var out []Crew
	if dir, err := CrewsDir(); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var c Crew
			if json.Unmarshal(data, &c) == nil && c.Name != "" && !IsBuiltin(c.Name) {
				out = append(out, c)
			}
		}
	}
	b := builtinCrews()
	names := make([]string, 0, len(b))
	for n := range b {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, b[n])
	}
	return out
}

// SaveCrew validates and writes a user crew.
func SaveCrew(c Crew) (string, error) {
	c.Builtin = false
	if err := c.Validate(); err != nil {
		return "", err
	}
	dir, err := CrewsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, deriveFileName(c.Name))
	return path, os.WriteFile(path, data, 0o600)
}
