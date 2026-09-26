// Package promptlib manages named prompt libraries as directories of
// plain-text files. Metadata (order and enabled state) is encoded in the
// filename: "NNN-slug.md" is an enabled entry at order NNN and "_NNN-slug.md"
// is a disabled one. A global library and a project-local library merge so
// project entries win by name, and a project entry sharing a global name
// takes the global entry's slot while keeping its own (project) identity.
package promptlib

import (
	"os"
	"path/filepath"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/filelib"
)

// Entry is one named prompt in a library.
type Entry struct {
	Name    string       // the on-disk slug, verbatim: "deploy-app"
	Prompt  string       // file body
	Order   int          // the NNN prefix, 1..999
	Enabled bool         // false when the basename starts with "_"
	Scope   config.Scope // global or project
	Path    string       // absolute path; the entry's identity
}

// Listing is the result of loading one scope's directory. Strays are
// basenames that do not parse as prompt files and are never read or touched.
type Listing struct {
	Entries []Entry  // sorted by (Order, Name)
	Strays  []string // sorted basenames
}

// ErrNameExists is returned by Create when the slug already exists in the
// target scope.
var ErrNameExists = filelib.ErrNameExists

// ErrLibraryFull is returned by Create when the scope already holds 999
// entries, the most the three-digit filename grammar can address.
var ErrLibraryFull = filelib.ErrLibraryFull

var spec = filelib.Spec{
	Ext:        ".md",
	TempPrefix: ".tmp-prompt-",
	GlobalDir:  config.GlobalPromptsDir,
	ProjectDir: config.ProjectPromptsDir,
}

func fromFilelib(e filelib.Entry) Entry {
	return Entry{
		Name:    e.Name,
		Prompt:  e.Body,
		Order:   e.Order,
		Enabled: e.Enabled,
		Scope:   e.Scope,
		Path:    e.Path,
	}
}

func toFilelib(e Entry) filelib.Entry {
	return filelib.Entry{
		Name:    e.Name,
		Body:    e.Prompt,
		Order:   e.Order,
		Enabled: e.Enabled,
		Scope:   e.Scope,
		Path:    e.Path,
	}
}

func entriesFromFilelib(in []filelib.Entry) []Entry {
	out := make([]Entry, len(in))
	for i, e := range in {
		out[i] = fromFilelib(e)
	}
	return out
}

func entriesToFilelib(in []Entry) []filelib.Entry {
	out := make([]filelib.Entry, len(in))
	for i, e := range in {
		out[i] = toFilelib(e)
	}
	return out
}

// Dir returns the prompt-library directory for a scope.
func Dir(scope config.Scope, workdir string) (string, error) {
	return spec.Dir(scope, workdir)
}

// Load reads one scope's directory.
func Load(scope config.Scope, workdir string) (Listing, error) {
	l, err := spec.Load(scope, workdir)
	if err != nil {
		return Listing{}, err
	}
	return Listing{Entries: entriesFromFilelib(l.Entries), Strays: l.Strays}, nil
}

// Slug converts a user-supplied name into the lowercase alnum-with-hyphens
// form the filename grammar demands.
func Slug(name string) (string, error) {
	return filelib.Slug(name)
}

// FileName renders a prompt filename for the given order, slug and enabled
// state.
func FileName(order int, slug string, enabled bool) string {
	return spec.FileName(order, slug, enabled)
}

// ParseFileName parses a basename against the strict filename grammar.
func ParseFileName(base string) (order int, slug string, enabled, ok bool) {
	return spec.ParseFileName(base)
}

// Create writes a new prompt into a scope.
func Create(scope config.Scope, workdir, name, prompt string) (Entry, error) {
	e, err := spec.Create(scope, workdir, name, prompt)
	if err != nil {
		return Entry{}, err
	}
	return fromFilelib(e), nil
}

// Update overwrites an entry's body in place, preserving its identity.
func Update(e Entry, prompt string) (Entry, error) {
	updated, err := spec.Update(toFilelib(e), prompt)
	if err != nil {
		return Entry{}, err
	}
	return fromFilelib(updated), nil
}

// SetEnabled toggles the disabled marker by renaming the file, leaving the
// body byte-identical.
func SetEnabled(e Entry, enabled bool) (Entry, error) {
	updated, err := spec.SetEnabled(toFilelib(e), enabled)
	if err != nil {
		return Entry{}, err
	}
	return fromFilelib(updated), nil
}

// Delete removes an entry's file.
func Delete(e Entry) error {
	return spec.Delete(toFilelib(e))
}

// Reorder moves the entry at index from to index to within a scope's sorted
// entries, then renumbers the whole scope to a fresh collision-free grid.
func Reorder(scope config.Scope, workdir string, entries []Entry, from, to int) ([]Entry, error) {
	moved, err := spec.Reorder(scope, workdir, entriesToFilelib(entries), from, to)
	if err != nil {
		return nil, err
	}
	return entriesFromFilelib(moved), nil
}

// Merge overlays project entries onto global entries.
func Merge(global, project []Entry) []Entry {
	return entriesFromFilelib(filelib.Merge(entriesToFilelib(global), entriesToFilelib(project)))
}

// Enabled returns the enabled entries, preserving order.
func Enabled(entries []Entry) []Entry {
	return entriesFromFilelib(filelib.Enabled(entriesToFilelib(entries)))
}

// Filter returns the entries whose name or prompt contains the query.
func Filter(entries []Entry, query string) []Entry {
	return entriesFromFilelib(filelib.Filter(entriesToFilelib(entries), query))
}

// Match reports whether an entry matches a query.
func Match(e Entry, query string) bool {
	return filelib.Match(toFilelib(e), query)
}

// Prompts returns just the prompt strings from a slice of entries.
func Prompts(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Prompt
	}
	return out
}

// writePrompt writes a prompt file directly, used by tests that pre-fill a
// directory. It does not use the atomic temp/rename dance; Create does.
func writePrompt(path, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body+"\n"), mode)
}

// ExtraEntries returns prompts from enabled plugins, named "plugin:slug".
// They are offered as /prompt:<name> but are not part of either editable
// library. nil means none. Set once at startup.
var ExtraEntries func() []Entry
