// Package filelib implements a generic file-backed library used by the prompt
// library and the process library. Entries live as plain files named
// "NNN-slug.EXT" for enabled entries and "_NNN-slug.EXT" for disabled ones.
// The library keeps an entry's body, order, enabled state, and scope identity
// on disk using atomic temp→fsync→chmod→rename writes. Global and project
// scopes may be merged; project entries override global entries by name.
package filelib

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/signet/internal/config"
)

// Spec describes one file-backed library: its extension and the two
// directories that hold its global and project scopes.
type Spec struct {
	// Ext is the filename extension including the leading dot, e.g. ".md".
	Ext string
	// TempPrefix is the prefix for temporary files created during atomic
	// writes, e.g. ".tmp-prompt-".
	TempPrefix string
	// GlobalDir returns the global-scope library directory. It may error
	// if the directory cannot be resolved.
	GlobalDir func() (string, error)
	// ProjectDir returns the project-scope library directory for a workdir.
	ProjectDir func(workdir string) string
}

// Entry is one item in a file-backed library.
type Entry struct {
	Name    string       // the on-disk slug, verbatim: "deploy-app"
	Body    string       // file body
	Order   int          // the NNN prefix, 1..999
	Enabled bool         // false when the basename starts with "_"
	Scope   config.Scope // global or project
	Path    string       // absolute path; the entry's identity
}

// Listing is the result of loading one scope's directory. Strays are
// basenames that do not parse as library files and are never read or touched.
type Listing struct {
	Entries []Entry  // sorted by (Order, Name)
	Strays  []string // sorted basenames
}

// ErrNameExists is returned by Create when the slug already exists in the
// target scope. The caller confirms and then calls Update.
var ErrNameExists = errors.New("library name already exists")

// ErrLibraryFull is returned by Create when the scope already holds 999
// entries, the most the three-digit filename grammar can address.
var ErrLibraryFull = errors.New("library is full")

// maxSlugRunes caps the slug length so the NNN prefix and extension suffix
// never overflow the filesystem-friendly filename width.
const maxSlugRunes = 64

// fileNameRe returns the strict filename grammar for the spec's extension.
func (s Spec) fileNameRe() *regexp.Regexp {
	return regexp.MustCompile(`^(_?)(\d{3})-([a-z0-9]+(?:-[a-z0-9]+)*)` + regexp.QuoteMeta(s.Ext) + `$`)
}

// Dir returns the library directory for a scope.
func (s Spec) Dir(scope config.Scope, workdir string) (string, error) {
	switch scope {
	case config.ScopeGlobal:
		return s.GlobalDir()
	case config.ScopeProject:
		return s.ProjectDir(workdir), nil
	default:
		return "", fmt.Errorf("unknown library scope %q", scope)
	}
}

// Load reads one scope's directory. A missing directory is an empty library,
// not an error. Files that do not parse — READMEs, editor swap files,
// sub-directories, crashed-renumber temps — are collected as strays and never
// read, renamed, or deleted.
func (s Spec) Load(scope config.Scope, workdir string) (Listing, error) {
	dir, err := s.Dir(scope, workdir)
	if err != nil {
		return Listing{}, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Listing{}, nil
	}
	if err != nil {
		return Listing{}, fmt.Errorf("read library dir %s: %w", dir, err)
	}
	re := s.fileNameRe()
	var l Listing
	for _, de := range entries {
		name := de.Name()
		isReg := de.Type().IsRegular()
		if !isReg {
			// Some filesystems report DT_UNKNOWN for freshly created files;
			// fall back to Info rather than treating them as strays.
			info, err := de.Info()
			if err == nil {
				isReg = info.Mode().IsRegular()
			}
		}
		if de.IsDir() || !isReg {
			l.Strays = append(l.Strays, name)
			continue
		}
		order, slug, enabled, ok := s.parseFileName(name, re)
		if !ok {
			l.Strays = append(l.Strays, name)
			continue
		}
		path := filepath.Join(dir, name)
		body, err := readEntry(path)
		if err != nil {
			return Listing{}, fmt.Errorf("read library entry %s: %w", path, err)
		}
		l.Entries = append(l.Entries, Entry{
			Name:    slug,
			Body:    body,
			Order:   order,
			Enabled: enabled,
			Scope:   scope,
			Path:    path,
		})
	}
	sortEntries(l.Entries)
	sort.Strings(l.Strays)
	return l, nil
}

// Slug converts a user-supplied name into the lowercase alnum-with-hyphens
// form the filename grammar demands. Underscores and every other non-alnum
// rune become a single interior hyphen, so a produced slug never contains "_"
// and cannot collide with the disabled marker.
func Slug(name string) (string, error) {
	var b strings.Builder
	prevDash := true // suppress a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("invalid library name %q: no letters or digits", name)
	}
	if len([]rune(slug)) > maxSlugRunes {
		return "", fmt.Errorf("invalid library name %q: slug longer than %d characters", name, maxSlugRunes)
	}
	return slug, nil
}

// FileName renders a library filename for the given order, slug and enabled
// state. The leading underscore is the disabled marker.
func (s Spec) FileName(order int, slug string, enabled bool) string {
	prefix := ""
	if !enabled {
		prefix = "_"
	}
	return fmt.Sprintf("%s%03d-%s%s", prefix, order, slug, s.Ext)
}

// ParseFileName parses a basename against the strict filename grammar. It
// rejects anything that does not parse, including four-digit orders, order
// zero, underscores inside the slug, uppercase slugs, and other extensions.
func (s Spec) ParseFileName(base string) (order int, slug string, enabled, ok bool) {
	return s.parseFileName(base, s.fileNameRe())
}

func (s Spec) parseFileName(base string, re *regexp.Regexp) (order int, slug string, enabled, ok bool) {
	m := re.FindStringSubmatch(base)
	if m == nil {
		return 0, "", false, false
	}
	order, err := strconv.Atoi(m[2])
	if err != nil || order < 1 || order > 999 {
		return 0, "", false, false
	}
	slug = m[3]
	if len([]rune(slug)) > maxSlugRunes {
		return 0, "", false, false
	}
	return order, slug, m[1] == "", true
}

// Create writes a new entry into a scope at order last+10, clamped to 999. A
// slug already present in that scope returns ErrNameExists rather than
// silently replacing it. When the next slot would overflow, the scope is
// renumbered first; a scope that already holds 999 entries returns
// ErrLibraryFull.
func (s Spec) Create(scope config.Scope, workdir, name, body string) (Entry, error) {
	slug, err := Slug(name)
	if err != nil {
		return Entry{}, err
	}
	dir, err := s.Dir(scope, workdir)
	if err != nil {
		return Entry{}, err
	}
	listing, err := s.Load(scope, workdir)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range listing.Entries {
		if e.Name == slug {
			return Entry{}, ErrNameExists
		}
	}
	if len(listing.Entries) >= 999 {
		return Entry{}, ErrLibraryFull
	}

	entries := listing.Entries
	order := 10
	if len(entries) > 0 {
		order = entries[len(entries)-1].Order + 10
	}
	if order > 999 {
		// Collapse gaps and reserve one slot's headroom so the new entry fits.
		step := renumberStep(len(entries) + 1)
		entries, err = s.renumber(scope, workdir, entries, step)
		if err != nil {
			return Entry{}, err
		}
		if len(entries) > 0 {
			order = entries[len(entries)-1].Order + step
		} else {
			order = step
		}
	}
	if order > 999 {
		return Entry{}, ErrLibraryFull
	}

	path := filepath.Join(dir, s.FileName(order, slug, true))
	if err := s.writeEntry(path, body, fileMode(scope)); err != nil {
		return Entry{}, err
	}
	return Entry{Name: slug, Body: body, Order: order, Enabled: true, Scope: scope, Path: path}, nil
}

// Update overwrites an entry's body in place, preserving its path, order,
// enabled state and scope.
func (s Spec) Update(e Entry, body string) (Entry, error) {
	if err := s.writeEntry(e.Path, body, fileMode(e.Scope)); err != nil {
		return Entry{}, err
	}
	e.Body = body
	return e, nil
}

// SetEnabled toggles the disabled marker by renaming the file, leaving the
// body byte-identical. It refuses to overwrite an unrelated file already at
// the target name.
func (s Spec) SetEnabled(e Entry, enabled bool) (Entry, error) {
	if e.Enabled == enabled {
		return e, nil
	}
	dir := filepath.Dir(e.Path)
	target := filepath.Join(dir, s.FileName(e.Order, e.Name, enabled))
	if _, err := os.Lstat(target); err == nil {
		return Entry{}, fmt.Errorf("rename target %s already exists", target)
	} else if !os.IsNotExist(err) {
		return Entry{}, err
	}
	if err := os.Rename(e.Path, target); err != nil {
		return Entry{}, err
	}
	e.Enabled = enabled
	e.Path = target
	return e, nil
}

// Delete removes an entry's file.
func (s Spec) Delete(e Entry) error {
	return os.Remove(e.Path)
}

// Reorder moves the entry at index from to index to within a scope's sorted
// entries, then renumbers the whole scope to a fresh collision-free grid. It
// returns the renumbered entries. Hand-picked order numbers are lost on the
// first reorder — the grid is what makes moves collision-free.
func (s Spec) Reorder(scope config.Scope, workdir string, entries []Entry, from, to int) ([]Entry, error) {
	if from < 0 || from >= len(entries) || to < 0 || to >= len(entries) {
		return nil, fmt.Errorf("reorder index out of range")
	}
	dir, err := s.Dir(scope, workdir)
	if err != nil {
		return nil, err
	}

	moved := make([]Entry, len(entries))
	copy(moved, entries)
	e := moved[from]
	moved = append(moved[:from], moved[from+1:]...)
	moved = append(moved[:to], append([]Entry{e}, moved[to:]...)...)

	step := renumberStep(len(moved))
	for i := range moved {
		order := (i + 1) * step
		if order > 999 {
			order = 999
		}
		moved[i].Order = order
	}

	origs := make([]string, len(entries))
	temps := make([]string, len(moved))
	finals := make([]string, len(moved))
	for i := range entries {
		origs[i] = entries[i].Path
	}
	for i := range moved {
		temps[i] = filepath.Join(dir, fmt.Sprintf(".signet-tmp-%d-%s%s", i, moved[i].Name, s.Ext))
		finals[i] = filepath.Join(dir, s.FileName(moved[i].Order, moved[i].Name, moved[i].Enabled))
	}

	// Phase 1: every source moves to a dot-prefixed temp name so a swap never
	// collides in a single pass. On failure, restore what already moved.
	if err := renameAll(origs, temps); err != nil {
		_ = renameAll(temps[:len(origs)], origs)
		return nil, err
	}

	// Phase 2 guard: every final target must be clear before any rename.
	// os.Rename overwrites silently on POSIX, so this check happens first and
	// aborts the whole reorder, rolling phase 1 back, if anything is there.
	for _, f := range finals {
		if _, err := os.Lstat(f); err == nil {
			_ = renameAll(temps, origs)
			return nil, fmt.Errorf("reorder collision at %s", f)
		} else if !os.IsNotExist(err) {
			_ = renameAll(temps, origs)
			return nil, err
		}
	}
	if err := renameAll(temps, finals); err != nil {
		// Best effort rollback: finals already written stay; report the error.
		return nil, err
	}
	for i := range moved {
		moved[i].Path = finals[i]
	}
	return moved, nil
}

// Merge overlays project entries onto global entries. Each scope is already
// sorted; the merged list is the global block in global order, then
// project-only names in project order. A project entry sharing a global name
// replaces the global entry's content but keeps the global slot — and keeps
// its own project identity, so a disabled project entry still vetoes the
// global one after Enabled filtering.
func Merge(global, project []Entry) []Entry {
	projectByName := make(map[string]Entry, len(project))
	for _, e := range project {
		projectByName[e.Name] = e
	}
	seen := make(map[string]bool, len(global)+len(project))
	out := make([]Entry, 0, len(global)+len(project))
	for _, g := range global {
		if p, ok := projectByName[g.Name]; ok {
			out = append(out, p)
		} else {
			out = append(out, g)
		}
		seen[g.Name] = true
	}
	for _, p := range project {
		if !seen[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

// Enabled returns the enabled entries, preserving order. It runs after Merge
// so a disabled project entry vetoes the global entry it shadows.
func Enabled(entries []Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Enabled {
			out = append(out, e)
		}
	}
	return out
}

// Filter returns the entries whose name or body contains the query. An empty
// query returns a copy of the slice.
func Filter(entries []Entry, query string) []Entry {
	if query == "" {
		out := make([]Entry, len(entries))
		copy(out, entries)
		return out
	}
	var out []Entry
	for _, e := range entries {
		if Match(e, query) {
			out = append(out, e)
		}
	}
	return out
}

// Match reports whether an entry matches a query. The match is
// case-insensitive against the name and body text.
func Match(e Entry, query string) bool {
	q := strings.ToLower(query)
	return strings.Contains(strings.ToLower(e.Name), q) ||
		strings.Contains(strings.ToLower(e.Body), q)
}

// readEntry reads an entry file, trimming exactly one trailing newline (and
// a CR before it, so CRLF files round-trip too). The whole file is the body,
// verbatim; the extension is for editor syntax highlighting only.
func readEntry(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	body := strings.TrimSuffix(string(data), "\n")
	body = strings.TrimSuffix(body, "\r")
	return body, nil
}

// writeEntry writes a library file atomically: temp file in the same
// directory, fsync, chmod, rename. The directory is created 0755 and the file
// gets the scope's mode (0600 global, 0644 project).
func (s Spec) writeEntry(path string, body string, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create library dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, s.TempPrefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(body + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename file: %w", err)
	}
	return nil
}

// fileMode returns the file mode for a scope: personal (0600) for global,
// team-readable (0644) for project.
func fileMode(scope config.Scope) os.FileMode {
	if scope == config.ScopeGlobal {
		return 0o600
	}
	return 0o644
}

// renumberStep returns the spacing used to renumber n entries. Small scopes
// keep the human-friendly 010/020/030 grid; larger scopes tighten the spacing
// so every order stays a three-digit 001..999 prefix.
func renumberStep(n int) int {
	if n <= 99 {
		return 10
	}
	if s := 999 / n; s >= 1 {
		return s
	}
	return 1
}

// renumber renames the entries of one scope onto a fresh grid with the given
// step, two phases so a swap never collides. The caller has already confirmed
// the targets are clear.
func (s Spec) renumber(scope config.Scope, workdir string, entries []Entry, step int) ([]Entry, error) {
	dir, err := s.Dir(scope, workdir)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		order := (i + 1) * step
		if order > 999 {
			order = 999
		}
		entries[i].Order = order
	}
	origs := make([]string, len(entries))
	temps := make([]string, len(entries))
	finals := make([]string, len(entries))
	for i := range entries {
		origs[i] = entries[i].Path
		temps[i] = filepath.Join(dir, fmt.Sprintf(".signet-tmp-%d-%s%s", i, entries[i].Name, s.Ext))
		finals[i] = filepath.Join(dir, s.FileName(entries[i].Order, entries[i].Name, entries[i].Enabled))
	}
	if err := renameAll(origs, temps); err != nil {
		_ = renameAll(temps, origs)
		return nil, err
	}
	for _, f := range finals {
		if _, err := os.Lstat(f); err == nil {
			_ = renameAll(temps, origs)
			return nil, fmt.Errorf("reorder collision at %s", f)
		} else if !os.IsNotExist(err) {
			_ = renameAll(temps, origs)
			return nil, err
		}
	}
	if err := renameAll(temps, finals); err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Path = finals[i]
	}
	return entries, nil
}

// renameAll renames srcs[i] to dsts[i] in order, stopping at the first error.
func renameAll(srcs, dsts []string) error {
	n := len(srcs)
	if len(dsts) < n {
		n = len(dsts)
	}
	for i := 0; i < n; i++ {
		if err := os.Rename(srcs[i], dsts[i]); err != nil {
			return err
		}
	}
	return nil
}

// sortEntries orders a slice by (Order, Name) so merge and the manager always
// see a stable, collision-free sequence.
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Order != entries[j].Order {
			return entries[i].Order < entries[j].Order
		}
		return entries[i].Name < entries[j].Name
	})
}
