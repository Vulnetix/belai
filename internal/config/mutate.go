package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// Scope selects which settings file a mutation targets.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// Document is a settings file opened for read-modify-write. It preserves
// unmanaged keys byte-for-byte so editing one setting cannot destroy another
// tool's configuration in the shared .vulnetix/settings.json namespace.
type Document struct {
	Path     string
	Settings Settings
	raw      map[string]json.RawMessage
}

// managedKeys returns the JSON names of every Settings field, derived by
// reflection so the list cannot drift from the struct.
func managedKeys() []string {
	t := reflect.TypeOf(Settings{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

// OpenSettings reads (or initialises) a settings file for read-modify-write.
func OpenSettings(scope Scope, workdir string) (*Document, error) {
	path, err := settingsPath(scope, workdir)
	if err != nil {
		return nil, err
	}
	d := &Document{Path: path, raw: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return d, nil
	}
	if err := json.Unmarshal(data, &d.raw); err != nil {
		return nil, fmt.Errorf("parse settings %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &d.Settings); err != nil {
		return nil, fmt.Errorf("parse settings %s: %w", path, err)
	}
	return d, nil
}

// Save writes the document back, setting managed keys and deleting the ones
// that unset, leaving every unmanaged key untouched. The write is atomic:
// temp file, fsync, rename.
func (d *Document) Save() error {
	data, err := json.Marshal(d.Settings)
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	for _, key := range managedKeys() {
		v, present := m[key]
		if !present || isUnsetJSON(v) {
			delete(d.raw, key)
			continue
		}
		d.raw[key] = v
	}

	out, err := json.MarshalIndent(d.raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	return writeSettingsFile(d.Path, out)
}

// Mutate opens a settings file, applies fn, and saves. Concurrent instances
// race with last-writer-wins; the atomic rename guarantees no torn file.
func Mutate(scope Scope, workdir string, fn func(*Settings) error) error {
	d, err := OpenSettings(scope, workdir)
	if err != nil {
		return err
	}
	if err := fn(&d.Settings); err != nil {
		return err
	}
	return d.Save()
}

func settingsPath(scope Scope, workdir string) (string, error) {
	if scope == ScopeGlobal {
		return GlobalSettingsPath()
	}
	return ProjectSettingsPath(workdir), nil
}

// isUnsetJSON reports whether a marshaled value represents "unset": a JSON
// null or an empty object. Value structs (PermissionRules) cannot use
// omitempty, so an empty object is their "unset" encoding.
func isUnsetJSON(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "null" || s == "{}"
}

// writeSettingsFile writes data atomically at 0600, preserving the existing
// directory modes (0700 global, 0755 project).
func writeSettingsFile(path string, data []byte) error {
	mode := os.FileMode(0o755)
	if gd, err := GlobalDir(); err == nil && filepath.Dir(path) == gd {
		mode = 0o700
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp settings file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp settings file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp settings file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp settings file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod settings file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename settings file: %w", err)
	}
	return nil
}
