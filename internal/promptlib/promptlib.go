// Package promptlib manages named prompt libraries. It supports a global
// library, a project-local override, and merge semantics where project
// entries win by name.
package promptlib

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
)

// Entry is one named prompt in the library.
type Entry struct {
	Name      string `json:"name"`
	Prompt    string `json:"prompt"`
	CreatedAt int64  `json:"created_at,omitempty"`
}

// Library holds named prompts.
type Library struct {
	Entries []Entry `json:"entries"`
}

// LoadGlobal reads the global prompts file. A missing file returns an empty
// library with no error.
func LoadGlobal() (Library, error) {
	path, err := config.GlobalPromptsPath()
	if err != nil {
		return Library{}, err
	}
	return load(path)
}

// LoadProject reads the project-local prompts file. A missing file returns
// an empty library with no error.
func LoadProject(workdir string) (Library, error) {
	return load(config.ProjectPromptsPath(workdir))
}

// SaveGlobal writes the global prompts file.
func SaveGlobal(lib Library) error {
	path, err := config.GlobalPromptsPath()
	if err != nil {
		return err
	}
	return save(path, lib)
}

// SaveProject writes the project-local prompts file.
func SaveProject(workdir string, lib Library) error {
	return save(config.ProjectPromptsPath(workdir), lib)
}

func load(path string) (Library, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Library{}, nil
	}
	if err != nil {
		return Library{}, fmt.Errorf("read prompts %s: %w", path, err)
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("parse prompts %s: %w", path, err)
	}
	return lib, nil
}

func save(path string, lib Library) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create prompts dir: %w", err)
	}
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal prompts: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write prompts %s: %w", path, err)
	}
	return nil
}

// Merge overlays project entries onto global entries by name: a project
// entry with the same name as a global entry wins.
func Merge(global, project Library) Library {
	byName := make(map[string]Entry, len(global.Entries))
	for _, e := range global.Entries {
		byName[e.Name] = e
	}
	for _, e := range project.Entries {
		byName[e.Name] = e
	}
	merged := make([]Entry, 0, len(byName))
	seen := make(map[string]bool, len(byName))
	for _, e := range global.Entries {
		if !seen[e.Name] {
			seen[e.Name] = true
			merged = append(merged, byName[e.Name])
		}
	}
	for _, e := range project.Entries {
		if !seen[e.Name] {
			seen[e.Name] = true
			merged = append(merged, byName[e.Name])
		}
	}
	return Library{Entries: merged}
}

// Match reports whether an entry matches a query. The match is
// case-insensitive against the name and prompt text.
func Match(e Entry, query string) bool {
	q := strings.ToLower(query)
	return strings.Contains(strings.ToLower(e.Name), q) ||
		strings.Contains(strings.ToLower(e.Prompt), q)
}

// Filter returns library entries whose name or prompt contains the query.
func (l Library) Filter(query string) []Entry {
	if query == "" {
		out := make([]Entry, len(l.Entries))
		copy(out, l.Entries)
		return out
	}
	var out []Entry
	for _, e := range l.Entries {
		if Match(e, query) {
			out = append(out, e)
		}
	}
	return out
}

// Prompts returns just the prompt strings from a slice of entries.
func Prompts(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Prompt
	}
	return out
}

// Add inserts a new entry, replacing an existing one with the same name.
// It sets CreatedAt when zero.
func (l *Library) Add(e Entry) {
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	for i, existing := range l.Entries {
		if existing.Name == e.Name {
			l.Entries[i] = e
			return
		}
	}
	l.Entries = append(l.Entries, e)
}

// Remove deletes the entry with the given name. It returns true when an
// entry was removed.
func (l *Library) Remove(name string) bool {
	for i, e := range l.Entries {
		if e.Name == name {
			l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
			return true
		}
	}
	return false
}
