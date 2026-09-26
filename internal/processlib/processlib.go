// Package processlib manages named libraries of supervised process commands as
// directories of shell-command files. It shares the prompt library's filename
// grammar — "NNN-slug.sh" is enabled, "_NNN-slug.sh" is disabled — where
// enabled additionally means the entry auto-starts when Belai opens the
// workdir. The whole file body is the command, verbatim.
package processlib

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filelib"
)

// Entry is one named process in a library.
type Entry struct {
	Name    string       // the on-disk slug, e.g. "llama-server"
	Command string       // file body, the command verbatim
	Order   int          // the NNN prefix, 1..999
	Enabled bool         // false when the basename starts with "_"
	Scope   config.Scope // global or project
	Path    string       // absolute path; the entry's identity
}

// Listing is the result of loading one scope's directory. Strays are
// basenames that do not parse as process files and are never read or touched.
type Listing struct {
	Entries []Entry
	Strays  []string
}

// ErrNameExists is returned by Create when the slug already exists.
var ErrNameExists = filelib.ErrNameExists

// ErrLibraryFull is returned when a scope already holds 999 entries.
var ErrLibraryFull = filelib.ErrLibraryFull

var spec = filelib.Spec{
	Ext:        ".sh",
	TempPrefix: ".tmp-process-",
	GlobalDir:  config.GlobalProcessesDir,
	ProjectDir: config.ProjectProcessesDir,
}

func fromFilelib(e filelib.Entry) Entry {
	return Entry{
		Name:    e.Name,
		Command: e.Body,
		Order:   e.Order,
		Enabled: e.Enabled,
		Scope:   e.Scope,
		Path:    e.Path,
	}
}

func toFilelib(e Entry) filelib.Entry {
	return filelib.Entry{
		Name:    e.Name,
		Body:    e.Command,
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

// Dir returns the process-library directory for a scope.
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

// NameFor derives a library slug from a supervised-process command. It takes
// the basename of argv[0], e.g. "llama-server -hf ..." becomes "llama-server".
func NameFor(command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	return filelib.Slug(filepath.Base(fields[0]))
}

// CreateUnique appends a process command to a scope. If an entry with an
// identical command already exists, that entry is returned unchanged. Otherwise
// a new entry is created with a slug derived from argv[0], suffixing "-2",
// "-3", ... until a free slug is found.
func CreateUnique(scope config.Scope, workdir, command string) (Entry, error) {
	listing, err := Load(scope, workdir)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range listing.Entries {
		if e.Command == command {
			return e, nil
		}
	}

	base, err := NameFor(command)
	if err != nil {
		return Entry{}, err
	}
	slug := base
	for i := 1; ; i++ {
		_, err := spec.Create(scope, workdir, slug, command)
		if err == nil {
			// Reload to get the entry identity disk gave it.
			l, err := Load(scope, workdir)
			if err != nil {
				return Entry{}, err
			}
			for _, e := range l.Entries {
				if e.Name == slug && e.Command == command {
					return e, nil
				}
			}
			return Entry{}, fmt.Errorf("created entry %q not found after reload", slug)
		}
		if !errors.Is(err, filelib.ErrNameExists) {
			return Entry{}, err
		}
		if i >= 999 {
			return Entry{}, ErrLibraryFull
		}
		slug = fmt.Sprintf("%s-%d", base, i+1)
	}
}

// Update overwrites an entry's body in place, preserving its identity.
func Update(e Entry, command string) (Entry, error) {
	updated, err := spec.Update(toFilelib(e), command)
	if err != nil {
		return Entry{}, err
	}
	return fromFilelib(updated), nil
}

// SetEnabled toggles the disabled marker, preserving the body byte-for-byte.
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

// Reorder reorders entries within a scope and renumbers them onto a fresh grid.
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

// Filter returns the entries whose name or command contains the query.
func Filter(entries []Entry, query string) []Entry {
	return entriesFromFilelib(filelib.Filter(entriesToFilelib(entries), query))
}

// Match reports whether an entry matches a query.
func Match(e Entry, query string) bool {
	return filelib.Match(toFilelib(e), query)
}
