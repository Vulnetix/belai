// Package plugins installs and loads plugin packages: a directory (usually a
// git repository) with a belai-plugin.json manifest that bundles skills,
// hooks, saved prompts and agent profiles. Every component is validated with
// the same validator Belai uses for the user's own files, installs are
// confirmed by the user after a full listing, and a git install is pinned to
// the commit that was shown. Plugins live only in the global state directory;
// a repository can never install or enable one.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/skills"
)

// ManifestFile is the manifest's name at a plugin's root.
const ManifestFile = "belai-plugin.json"

// Manifest is a plugin's belai-plugin.json. Each component list names
// directories relative to the plugin root:
//   - skills: directories of <name>/SKILL.md
//   - hooks: directories of *.json hook files
//   - prompts: directories of *.md prompts (the file name is the prompt name)
//   - agents: directories of *.json agent profiles
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`
	Prompts     []string `json:"prompts,omitempty"`
	Agents      []string `json:"agents,omitempty"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// reservedNames cannot be plugin names: they would read as Belai's own.
var reservedNames = map[string]bool{"belai": true, "user": true, "builtin": true}

// ValidName reports whether name is usable as a plugin name.
func ValidName(name string) bool { return nameRE.MatchString(name) && !reservedNames[name] }

// ReadManifest strictly decodes root/belai-plugin.json.
func ReadManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestFile))
	if err != nil {
		return Manifest{}, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("invalid %s: %w", ManifestFile, err)
	}
	if !ValidName(m.Name) {
		return Manifest{}, fmt.Errorf("plugin name %q must be lowercase letters, digits and hyphens (at most 40) and not reserved", m.Name)
	}
	return m, nil
}

// inside resolves rel against root and confirms it stays inside root after
// following symlinks. rel must be relative.
func inside(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("component path %q must be relative", rel)
	}
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	p, err := filepath.EvalSymlinks(filepath.Join(absRoot, rel))
	if err != nil {
		return "", fmt.Errorf("component path %q: %w", rel, err)
	}
	r, err := filepath.Rel(absRoot, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("component path %q escapes the plugin", rel)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", fmt.Errorf("component path %q is not a directory", rel)
	}
	return p, nil
}

// HookInfo describes one hook for the install listing.
type HookInfo struct {
	Name, Event, Command, Matcher string
}

// Summary is everything a plugin would add, for the user to confirm.
type Summary struct {
	Manifest Manifest
	Skills   []string
	Hooks    []HookInfo
	Prompts  []string
	Agents   []string
}

// Validate checks every component of the plugin at root and returns what it
// would add. Any invalid component fails the whole plugin: a plugin is never
// half-installed.
func Validate(root string) (Summary, error) {
	m, err := ReadManifest(root)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Manifest: m}
	for _, rel := range m.Skills {
		dir, err := inside(root, rel)
		if err != nil {
			return Summary{}, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Summary{}, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			sm, err := skills.ValidateSkill(string(data))
			if err != nil {
				return Summary{}, fmt.Errorf("skill %s: %w", e.Name(), err)
			}
			s.Skills = append(s.Skills, sm.Name)
		}
	}
	for _, rel := range m.Hooks {
		dir, err := inside(root, rel)
		if err != nil {
			return Summary{}, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Summary{}, err
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return Summary{}, err
			}
			h, err := hooks.ParseHookFile(data)
			if err != nil {
				return Summary{}, fmt.Errorf("hook %s: %w", e.Name(), err)
			}
			s.Hooks = append(s.Hooks, HookInfo{Name: h.Name, Event: h.Event, Command: h.Command, Matcher: h.Matcher})
		}
	}
	for _, rel := range m.Prompts {
		dir, err := inside(root, rel)
		if err != nil {
			return Summary{}, err
		}
		for _, p := range readPrompts(dir) {
			s.Prompts = append(s.Prompts, p.Name)
		}
	}
	for _, rel := range m.Agents {
		dir, err := inside(root, rel)
		if err != nil {
			return Summary{}, err
		}
		profs, err := readProfiles(dir, m.Name)
		if err != nil {
			return Summary{}, err
		}
		for _, p := range profs {
			s.Agents = append(s.Agents, p.Name)
		}
	}
	return s, nil
}

// Prompt is one plugin prompt.
type Prompt struct {
	Name   string // "plugin:slug"
	Prompt string
}

var promptNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func readPrompts(dir string) []Prompt {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Prompt
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		if !promptNameRE.MatchString(slug) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || len(data) > 64*1024 {
			continue
		}
		out = append(out, Prompt{Name: slug, Prompt: string(data)})
	}
	return out
}

func readProfiles(dir, plugin string) ([]agentprofile.AgentProfile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []agentprofile.AgentProfile
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var p agentprofile.AgentProfile
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("agent %s: %w", e.Name(), err)
		}
		p.Name = plugin + ":" + p.Name
		p.File = ""
		p.Builtin = false
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("agent %s: %w", e.Name(), err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Record is one installed plugin in the registry file.
type Record struct {
	Name      string    `json:"name"`
	Source    string    `json:"source"`
	Commit    string    `json:"commit"`
	Version   string    `json:"version,omitempty"`
	Enabled   bool      `json:"enabled"`
	Installed time.Time `json:"installed"`
}

// Dir is where plugins are stored.
func Dir() (string, error) {
	g, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(g, "plugins"), nil
}

func registryPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "plugins.json"), nil
}

// List returns the installed plugins in name order.
func List() ([]Record, error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("read plugin registry: %w", err)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	return recs, nil
}

func save(recs []Record) error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Root returns an installed plugin's directory.
func Root(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid plugin name %q", name)
	}
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// SetEnabled turns a plugin on or off.
func SetEnabled(name string, on bool) error {
	recs, err := List()
	if err != nil {
		return err
	}
	for i := range recs {
		if recs[i].Name == name {
			recs[i].Enabled = on
			return save(recs)
		}
	}
	return fmt.Errorf("plugin %q is not installed", name)
}

// Remove deletes a plugin and its registry entry.
func Remove(name string) error {
	recs, err := List()
	if err != nil {
		return err
	}
	var kept []Record
	found := false
	for _, r := range recs {
		if r.Name == name {
			found = true
			continue
		}
		kept = append(kept, r)
	}
	if !found {
		return fmt.Errorf("plugin %q is not installed", name)
	}
	root, err := Root(name)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return save(kept)
}

// enabled returns each enabled plugin's root and manifest. A plugin whose
// manifest no longer reads is skipped.
func enabled() []struct {
	root string
	m    Manifest
} {
	recs, err := List()
	if err != nil {
		return nil
	}
	var out []struct {
		root string
		m    Manifest
	}
	for _, r := range recs {
		if !r.Enabled {
			continue
		}
		root, err := Root(r.Name)
		if err != nil {
			continue
		}
		m, err := ReadManifest(root)
		if err != nil || m.Name != r.Name {
			continue
		}
		out = append(out, struct {
			root string
			m    Manifest
		}{root, m})
	}
	return out
}

// SkillRoots returns the skill directories of every enabled plugin,
// namespaced by plugin name.
func SkillRoots() []skills.Root {
	var out []skills.Root
	for _, p := range enabled() {
		for _, rel := range p.m.Skills {
			if dir, err := inside(p.root, rel); err == nil {
				out = append(out, skills.Root{Dir: dir, Namespace: p.m.Name})
			}
		}
	}
	return out
}

// Hooks returns the validated hooks of every enabled plugin. Each hook runs
// from its own directory inside the plugin and is named "plugin:name".
func Hooks(pol posture.Policy) []*hooks.Hook {
	var out []*hooks.Hook
	for _, p := range enabled() {
		for _, rel := range p.m.Hooks {
			dir, err := inside(p.root, rel)
			if err != nil {
				continue
			}
			hs, err := hooks.LoadDir(dir, pol)
			if err != nil {
				continue
			}
			for _, h := range hs {
				h.Name = p.m.Name + ":" + h.Name
				out = append(out, h)
			}
		}
	}
	return out
}

// Prompts returns the prompts of every enabled plugin, named "plugin:slug".
func Prompts() []Prompt {
	var out []Prompt
	for _, p := range enabled() {
		for _, rel := range p.m.Prompts {
			if dir, err := inside(p.root, rel); err == nil {
				for _, pr := range readPrompts(dir) {
					pr.Name = p.m.Name + ":" + pr.Name
					out = append(out, pr)
				}
			}
		}
	}
	return out
}

// Profiles returns the agent profiles of every enabled plugin, named
// "plugin:name". An invalid profile is skipped.
func Profiles() []agentprofile.AgentProfile {
	var out []agentprofile.AgentProfile
	for _, p := range enabled() {
		for _, rel := range p.m.Agents {
			if dir, err := inside(p.root, rel); err == nil {
				if profs, err := readProfiles(dir, p.m.Name); err == nil {
					out = append(out, profs...)
				}
			}
		}
	}
	return out
}
