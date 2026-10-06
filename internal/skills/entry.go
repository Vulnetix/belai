package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/posture"
)

// MaxDescriptionBytes is the longest description a skill Compose writes, the
// specification's limit.
const MaxDescriptionBytes = 1024

// Entry is one discovered skill: its validated manifest and where its
// SKILL.md lives. Name is the name the model and the user use; a skill from
// a plugin is namespaced "plugin:name".
type Entry struct {
	Manifest
	Name string
	Path string
	// Source is "user" for the global skills directory, BuiltinSource for a skill
	// that ships with Belai, else the plugin name.
	Source string
	// Embedded is true for a skill compiled into Belai, whose text is read from
	// the binary and not from Path.
	Embedded bool
}

// Root is a directory of <name>/SKILL.md skills. Namespace is empty for the
// user's own skills and the plugin name for a plugin's.
type Root struct {
	Dir       string
	Namespace string
}

// Discover loads every valid skill under roots, in name order. On a name
// clash the earlier root wins, so the user's own skills (listed first) shadow
// a plugin's. Invalid skills are skipped, never half-loaded.
func Discover(roots []Root, pol posture.Policy) []Entry {
	seen := map[string]bool{}
	var out []Entry
	for _, r := range roots {
		dirents, err := os.ReadDir(r.Dir)
		if err != nil {
			continue
		}
		for _, d := range dirents {
			if !d.IsDir() {
				continue
			}
			path := filepath.Join(r.Dir, d.Name(), "SKILL.md")
			data, err := readSkillFile(path)
			if err != nil {
				continue
			}
			m, err := ValidateWithPosture(string(data), pol)
			if err != nil || m == nil || m.Name == "" {
				continue
			}
			name := m.Name
			source := "user"
			if r.Namespace != "" {
				name = r.Namespace + ":" + m.Name
				source = r.Namespace
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Entry{Manifest: *m, Name: name, Path: path, Source: source})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find returns the entry named name (case-insensitive).
func Find(entries []Entry, name string) (Entry, bool) {
	for _, e := range entries {
		if strings.EqualFold(e.Name, strings.TrimSpace(name)) {
			return e, true
		}
	}
	return Entry{}, false
}

// ReadBody re-reads and re-validates e's SKILL.md and returns the text after
// the front matter. The file may have changed since discovery, so it is
// validated again: an invalid file yields an error, never a body.
func ReadBody(e Entry) (string, error) {
	var doc string
	if e.Embedded {
		d, err := BuiltinDoc(e.Name)
		if err != nil {
			return "", err
		}
		doc = d
	} else {
		data, err := readSkillFile(e.Path)
		if err != nil {
			return "", err
		}
		doc = string(data)
	}
	if _, err := ValidateSkill(doc); err != nil {
		return "", fmt.Errorf("skill %q no longer validates: %w", e.Name, err)
	}
	_, body, err := Split(doc)
	if err != nil {
		return "", err
	}
	return body, nil
}

// ValidName reports whether name is usable as a new skill's name and
// directory: lowercase letters, digits and single hyphens, not starting or
// ending with one, at most 64 (the specification's rule).
func ValidName(name string) bool { return ValidSpecName(name) }

// Compose builds a SKILL.md document from its parts and validates it. The
// description is flattened to one line so it cannot add front-matter keys.
func Compose(name, description, body string) (string, error) {
	if ReservedName(name) {
		return "", fmt.Errorf("skill names starting with %q are reserved for Belai's own skills", BuiltinPrefix)
	}
	if !ValidName(name) {
		return "", fmt.Errorf("skill name %q must be lowercase letters, digits and single hyphens, not starting or ending with one (at most 64)", name)
	}
	description = strings.Join(strings.Fields(description), " ")
	if description == "" {
		return "", fmt.Errorf("skill description is required")
	}
	if len(description) > MaxDescriptionBytes {
		return "", fmt.Errorf("skill description is longer than %d bytes", MaxDescriptionBytes)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("skill body is required")
	}
	doc := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	m, err := ValidateSkill(doc)
	if err != nil {
		return "", err
	}
	// What is written must load as what the user approved.
	if m.Description != description || m.Name != name {
		return "", fmt.Errorf("the description does not read back as written (a # or other YAML syntax changed it); reword it")
	}
	return doc, nil
}

// maxSkillFile bounds a SKILL.md the loader reads: it is read every turn, and a
// skill is a page of instructions, so a file far past the library's 32 KiB is not one.
const maxSkillFile = 1 << 20

func readSkillFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSkillFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSkillFile {
		return nil, fmt.Errorf("%s is over %d bytes", filepath.Base(path), maxSkillFile)
	}
	return data, nil
}

// Invalidate drops the LoadDir cache, so a skill written this process shows
// up in the next listing.
func Invalidate() {
	loadMu.Lock()
	loadValSet = false
	loadMu.Unlock()
}
