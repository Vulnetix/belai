package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// credentialFileEntry is one field entry in the JSON file.
type credentialFileEntry struct {
	Source  string `json:"source"`
	Name    string `json:"name,omitempty"`
	Value   string `json:"value,omitempty"`
	Account string `json:"account,omitempty"`
}

// credentialFile is the on-disk JSON shape.
type credentialFile struct {
	Version            int                                       `json:"version"`
	Providers          map[string]map[string]credentialFileEntry `json:"providers"`
	AllowInsecurePerms bool                                      `json:"allow_insecure_perms,omitempty"`
}

type fileStore struct {
	path          string
	isProject     bool
	allowInsecure bool
}

func newFileStore(path string, isProject bool) *fileStore {
	return &fileStore{path: path, isProject: isProject}
}

func (f *fileStore) load() (*credentialFile, string, error) {
	data, path, err := f.readFile()
	if errors.Is(err, os.ErrNotExist) {
		return &credentialFile{Version: 1, Providers: map[string]map[string]credentialFileEntry{}}, f.path, nil
	}
	if err != nil {
		return nil, "", err
	}
	var cf credentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if cf.Providers == nil {
		cf.Providers = map[string]map[string]credentialFileEntry{}
	}
	if cf.AllowInsecurePerms {
		f.allowInsecure = true
	}
	return &cf, path, nil
}

func (f *fileStore) readFile() ([]byte, string, error) {
	// If the literal path is a regular file (not a directory), read it directly.
	info, err := os.Stat(f.path)
	if err == nil && !info.IsDir() {
		data, err := os.ReadFile(f.path)
		return data, f.path, err
	}
	// Accommodation: if the parent is a regular file, read it (global dir as file).
	if !f.isProject {
		parent := filepath.Dir(f.path)
		pi, err := os.Stat(parent)
		if err == nil && !pi.IsDir() {
			data, err := os.ReadFile(parent)
			return data, parent, err
		}
	}
	data, err := os.ReadFile(f.path)
	return data, f.path, err
}

func (f *fileStore) read(provider, field string, spec []Field) (v Value, ok bool, note string) {
	cf, path, err := f.load()
	if err != nil {
		return Value{}, false, ""
	}
	p, ok := cf.Providers[provider]
	if !ok {
		return Value{}, false, ""
	}
	entry, ok := p[field]
	if !ok {
		return Value{}, false, ""
	}

	return f.resolveEntry(entry, provider, field, spec, path)
}

func (f *fileStore) resolveEntry(entry credentialFileEntry, provider, field string, spec []Field, effPath string) (Value, bool, string) {
	isSecret := false
	for _, sp := range spec {
		if sp.Name == field {
			isSecret = sp.Secret
			break
		}
	}

	if entry.Source == "inline" {
		if f.isProject && isSecret {
			return Value{}, false, fmt.Sprintf("inline secret rejected in project file for %s:%s", provider, field)
		}
		if !f.allowInsecure && runtime.GOOS != "windows" {
			info, err := os.Stat(effPath)
			if err == nil && !info.IsDir() {
				if info.Mode().Perm()&0o077 != 0 {
					return Value{}, false, fmt.Sprintf("file mode %04o rejected (allow_insecure_perms to override)", info.Mode().Perm())
				}
			} else if err == nil && info.IsDir() {
				if info.Mode().Perm()&0o077 != 0 {
					return Value{}, false, fmt.Sprintf("directory mode %04o rejected", info.Mode().Perm())
				}
			}
		}
		return Value{
			Field: field, Location: effPath, Source: f.source(), Secret: isSecret, value: entry.Value,
		}, true, ""
	}

	if entry.Source == "env" && entry.Name != "" {
		if v := os.Getenv(entry.Name); v != "" {
			return Value{
				Field: field, Location: "$" + entry.Name, Source: SourceEnv, Secret: isSecret, value: v,
			}, true, ""
		}
	}

	if entry.Source == "keychain" && entry.Account != "" {
		// Defer to keychain resolver; not handled here.
		return Value{}, false, ""
	}

	return Value{}, false, ""
}

func (f *fileStore) source() Source {
	if f.isProject {
		return SourceProjectFile
	}
	return SourceUserFile
}

func (f *fileStore) write(provider, field, secret string) error {
	cf, _, err := f.load()
	if err != nil {
		return err
	}
	p, ok := cf.Providers[provider]
	if !ok {
		p = map[string]credentialFileEntry{}
		cf.Providers[provider] = p
	}
	p[field] = credentialFileEntry{Source: "inline", Value: secret}
	return f.save(cf)
}

// writeEnvRef writes a credential entry that references an environment
// variable rather than holding the value. A reference is not a secret, so it
// is legal in a project credential file.
func (f *fileStore) writeEnvRef(provider, field, envName string) error {
	cf, _, err := f.load()
	if err != nil {
		return err
	}
	p, ok := cf.Providers[provider]
	if !ok {
		p = map[string]credentialFileEntry{}
		cf.Providers[provider] = p
	}
	p[field] = credentialFileEntry{Source: "env", Name: envName}
	return f.save(cf)
}

func (f *fileStore) delete(provider, field string) error {
	cf, _, err := f.load()
	if err != nil {
		return err
	}
	p, ok := cf.Providers[provider]
	if !ok {
		return nil
	}
	delete(p, field)
	if len(p) == 0 {
		delete(cf.Providers, provider)
	}
	return f.save(cf)
}

func (f *fileStore) save(cf *credentialFile) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp credential file: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename credential file: %w", err)
	}
	return nil
}

func (f *fileStore) exists() bool {
	_, err := os.Stat(f.path)
	return err == nil
}
