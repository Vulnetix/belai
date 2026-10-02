// Package processlib manages named libraries of supervised process commands as
// directories of files. It shares the prompt library's filename grammar —
// "NNN-slug.ext" is enabled, "_NNN-slug.ext" is disabled — where enabled
// additionally means the entry auto-starts when Belai opens the workdir.
//
// An entry is one of two forms. A legacy entry is "NNN-slug.sh": the whole file
// body is a shell command, run as `sh -c`, verbatim. A structured entry is
// "NNN-slug.json": a JSON document (internal/libitem.ProcessDoc) of a command, its
// arguments, options, environment, working directory, user and redirects, run as
// an argv with no shell. Both load, list, toggle, reorder and delete the same way;
// when one slug has both files the structured one wins and the other is a stray.
package processlib

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filelib"
	"github.com/vulnetix/belai/internal/libitem"
)

// Entry is one named process in a library.
type Entry struct {
	Name    string       // the on-disk slug, e.g. "llama-server"
	Command string       // the shell command verbatim; for a structured entry, its argv for display
	Order   int          // the NNN prefix, 1..999
	Enabled bool         // false when the basename starts with "_"
	Scope   config.Scope // global or project
	Path    string       // absolute path; the entry's identity
	// Spec is the structured form of an entry stored as NNN-slug.json, with its
	// name set to the slug. nil for a legacy shell-string entry.
	Spec *libitem.ProcessDoc
	// Body is the file's text verbatim: the shell command, or the JSON.
	Body string
}

// Structured reports whether the entry is a structured (argv) process.
func (e Entry) Structured() bool { return e.Spec != nil }

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
	AltExts:    []string{jsonExt},
	TempPrefix: ".tmp-process-",
	GlobalDir:  config.GlobalProcessesDir,
	ProjectDir: config.ProjectProcessesDir,
}

// jsonExt is the extension of a structured entry.
const jsonExt = ".json"

func fromFilelib(e filelib.Entry) Entry {
	return Entry{
		Name:    e.Name,
		Command: e.Body,
		Body:    e.Body,
		Order:   e.Order,
		Enabled: e.Enabled,
		Scope:   e.Scope,
		Path:    e.Path,
	}
}

func toFilelib(e Entry) filelib.Entry {
	body := e.Command
	if e.Spec != nil {
		body = e.Body
	}
	return filelib.Entry{
		Name:    e.Name,
		Body:    body,
		Order:   e.Order,
		Enabled: e.Enabled,
		Scope:   e.Scope,
		Path:    e.Path,
		Ext:     filepath.Ext(e.Path),
	}
}

// entriesFromFilelib converts entries back, restoring what a filelib entry cannot
// carry (the structured form) from the entries they came from, matched by path
// and, for a reorder that moved the file, by scope and name.
func entriesFromFilelib(in []filelib.Entry, from ...[]Entry) []Entry {
	byPath := map[string]Entry{}
	byName := map[string]Entry{}
	for _, list := range from {
		for _, e := range list {
			byPath[e.Path] = e
			byName[string(e.Scope)+"/"+e.Name] = e
		}
	}
	out := make([]Entry, len(in))
	for i, e := range in {
		out[i] = fromFilelib(e)
		k, ok := byPath[e.Path]
		if !ok {
			k, ok = byName[string(e.Scope)+"/"+e.Name]
		}
		if ok && k.Spec != nil {
			out[i].Spec, out[i].Command, out[i].Body = k.Spec, k.Command, k.Body
		}
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

// Load reads one scope's directory. A structured file that is not a valid process
// document is a stray: listed, never run, never touched.
func Load(scope config.Scope, workdir string) (Listing, error) {
	l, err := spec.Load(scope, workdir)
	if err != nil {
		return Listing{}, err
	}
	out := Listing{Strays: l.Strays}
	for _, fe := range l.Entries {
		e := fromFilelib(fe)
		if fe.Ext == jsonExt {
			doc, err := ParseStructured(fe.Body, fe.Name, fe.Order, fe.Enabled)
			if err != nil {
				out.Strays = append(out.Strays, filepath.Base(fe.Path))
				continue
			}
			e.Spec, e.Command = &doc, doc.Display()
		}
		out.Entries = append(out.Entries, e)
	}
	sort.Strings(out.Strays)
	return out, nil
}

// ParseStructured reads the text of a structured entry named slug, at the given
// order and state, as the document it is (see libitem.NormalizeProcessFile).
func ParseStructured(body, slug string, order int, enabled bool) (libitem.ProcessDoc, error) {
	it, err := libitem.NormalizeProcessFile([]byte(body), slug, order, enabled)
	if err != nil {
		return libitem.ProcessDoc{}, err
	}
	return libitem.ParseProcess(it.Doc)
}

// PutDoc writes a process document at the scope, as NNN-slug.json, replacing the
// entry of that name whether it is structured or a legacy shell string. The
// document is kept as it is (indented, with the same keys), so what the library
// holds is what the host exports. Its order (0 means keep the existing one, or go
// last) and enabled state choose the file name. The name must already be a slug.
func PutDoc(scope config.Scope, workdir string, raw []byte) (Entry, error) {
	it, err := libitem.Validate(libitem.Process, raw)
	if err != nil {
		return Entry{}, err
	}
	doc, err := libitem.ParseProcess(it.Doc)
	if err != nil {
		return Entry{}, err
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, bytes.TrimSpace(it.Doc), "", "  "); err != nil {
		return Entry{}, err
	}
	fe, err := spec.PutExt(scope, workdir, doc.Name, indented.String(), doc.OrderOr(0), doc.IsEnabled(), jsonExt)
	if err != nil {
		return Entry{}, err
	}
	e := fromFilelib(fe)
	e.Spec, e.Command = &doc, doc.Display()
	return e, nil
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

// Update overwrites an entry's body in place, preserving its identity. For a
// structured entry the body is its JSON and must be a valid process document;
// for a legacy entry it is the shell command.
func Update(e Entry, body string) (Entry, error) {
	if e.Spec == nil {
		updated, err := spec.Update(toFilelib(e), body)
		if err != nil {
			return Entry{}, err
		}
		return fromFilelib(updated), nil
	}
	doc, err := ParseStructured(body, e.Name, e.Order, e.Enabled)
	if err != nil {
		return Entry{}, err
	}
	updated, err := spec.Update(toFilelib(e), body)
	if err != nil {
		return Entry{}, err
	}
	out := fromFilelib(updated)
	out.Spec, out.Command = &doc, doc.Display()
	return out, nil
}

// SetEnabled toggles the disabled marker, preserving the body byte-for-byte.
func SetEnabled(e Entry, enabled bool) (Entry, error) {
	updated, err := spec.SetEnabled(toFilelib(e), enabled)
	if err != nil {
		return Entry{}, err
	}
	return entriesFromFilelib([]filelib.Entry{updated}, []Entry{e})[0], nil
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
	return entriesFromFilelib(moved, entries), nil
}

// Merge overlays project entries onto global entries.
func Merge(global, project []Entry) []Entry {
	return entriesFromFilelib(filelib.Merge(entriesToFilelib(global), entriesToFilelib(project)), global, project)
}

// Enabled returns the enabled entries, preserving order.
func Enabled(entries []Entry) []Entry {
	return entriesFromFilelib(filelib.Enabled(entriesToFilelib(entries)), entries)
}

// Filter returns the entries whose name or command contains the query.
func Filter(entries []Entry, query string) []Entry {
	return entriesFromFilelib(filelib.Filter(entriesToFilelib(entries), query), entries)
}

// Match reports whether an entry matches a query.
func Match(e Entry, query string) bool {
	return filelib.Match(toFilelib(e), query)
}
