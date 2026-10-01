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
	// ID is the crew's own lowercase UUID, which follows it through a library
	// backup and install so a rename does not make it another crew. `belai rc`
	// stamps one on a crew that has none (EnsureCrewIDs). It is the first field
	// so CanonicalJSON matches the library's own encoding.
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []Member `json:"members"`
	// OnePerRepo refuses a start while a live worker of this crew already
	// works the same repository, so one run never doubles another's work.
	OnePerRepo bool `json:"one_per_repo,omitempty"`
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
	if c.ID != "" && !ValidID(c.ID) {
		return "", fmt.Errorf("crew id %q is not a lowercase UUID", c.ID)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, deriveFileName(c.Name))
	return path, writeAtomic(path, data)
}

// CanonicalJSON is the crew as the library stores it: the id, name,
// description and members in a fixed order, two-space indented and newline
// terminated. The daemon hashes this form for the automatic sync and the server
// re-encodes what it receives the same way (vdb-site belaiCanonicalCrew), so
// equal crews have equal bytes whatever the file on disk looks like.
func (c Crew) CanonicalJSON() ([]byte, error) {
	c.Builtin = false
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// userCrews reads every crew file in the user's directory leniently: a file
// that does not parse is skipped. Unlike LoadCrew it does not validate, so a
// crew whose members are not installed here is still listed.
func userCrews() ([]Crew, []string, error) {
	dir, err := CrewsDir()
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var crews []Crew
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var c Crew
		if json.Unmarshal(data, &c) != nil || c.Name == "" || IsBuiltin(c.Name) {
			continue
		}
		crews = append(crews, c)
		files = append(files, e.Name())
	}
	return crews, files, nil
}

// EnsureCrewIDs gives every stored user crew that lacks an id one and rewrites
// it in place, returning how many it stamped. Run when the rc daemon starts,
// like EnsureIDs, so a crew has the same identity every time the website sees it.
func EnsureCrewIDs() (int, error) {
	crews, files, err := userCrews()
	if err != nil {
		return 0, err
	}
	dir, err := CrewsDir()
	if err != nil {
		return 0, err
	}
	n := 0
	for i, c := range crews {
		if ValidID(c.ID) {
			continue
		}
		if c.ID, err = NewID(); err != nil {
			return n, err
		}
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return n, err
		}
		if err := writeAtomic(filepath.Join(dir, files[i]), data); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// StoredCrews lists the user's crews as they are on disk, without checking
// that their members exist here. Built-in crews are not included.
func StoredCrews() []Crew {
	crews, _, _ := userCrews()
	return crews
}

// CrewByID finds a stored user crew by its id.
func CrewByID(id string) (Crew, bool) {
	if !ValidID(id) {
		return Crew{}, false
	}
	for _, c := range StoredCrews() {
		if c.ID == id {
			return c, true
		}
	}
	return Crew{}, false
}

// DeleteCrew removes a user crew. A built-in crew is never removed.
func DeleteCrew(name string) error {
	if IsBuiltin(name) {
		return fmt.Errorf("crew %q is built in", name)
	}
	dir, err := CrewsDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, deriveFileName(name)))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no crew named %q", name)
	}
	return err
}
