package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/sanitize"
)

// The Outline and Symbol tools serve the session's symbol index
// (internal/symindex, docs/symbol-map.md): the declarations of the working
// directory's eligible files, parsed by the harness. The model chooses which
// file or name to ask about and nothing else; no argument reaches a parser, a
// path outside the session root or a limit above the user's setting.
//
// The declared names are the repository's own text, so a result is
// KindSymbol, which classifies. The session behind SymbolSource also drops
// rows from a file the classifier withheld a Read of, and rows a Read deny
// rule covers.

// OutlineName and SymbolName are the tools' names.
const (
	OutlineName = "Outline"
	SymbolName  = "Symbol"
)

// symbolNameMax bounds a name argument and a printed name.
const symbolNameMax = 120

// SymbolRow is one declaration in an answer.
type SymbolRow struct {
	Path      string
	Name      string
	Kind      string
	Container string
	Line      int
	EndLine   int
}

// SymbolWithheld counts the rows of one file an answer left out because an
// earlier read of that file was classified unsafe.
type SymbolWithheld struct {
	Path    string
	Rows    int
	Verdict string
}

// SymbolAnswer is what the session's index returned for one call.
type SymbolAnswer struct {
	Rows     []SymbolRow
	Withheld []SymbolWithheld
	// Unavailable says the index could not be built or read in time. No partial
	// rows ride with it.
	Unavailable bool
	// NotIndexed says an Outline named a file the index does not hold.
	NotIndexed bool
	// Files is how many files the index holds and Eligible how many source
	// files the walk found; Truncated says a bound stopped the index short.
	Files, Eligible int
	Truncated       bool
}

// SymbolSource is the session's symbol index as the tools see it. The session
// implements it and applies the flagged-file and Read-deny filters, so the
// tools never hold a path to the index or a verdict.
type SymbolSource interface {
	// Outline returns the declarations of the file at rel, a slash-separated
	// path under the session root.
	Outline(ctx context.Context, rel string) SymbolAnswer
	// Lookup returns up to limit declarations of name, best match first.
	Lookup(ctx context.Context, name, kind string, limit int) SymbolAnswer
}

// SymbolHub holds the session's SymbolSource. The tools are built before the
// session exists, so they hold the hub and the session fills it, as it does
// the knowledge hub. The zero value is empty and nil-safe, and a tool with no
// source answers that the index is not available.
type SymbolHub struct {
	mu sync.RWMutex
	s  SymbolSource
}

// Set installs (or, with nil, removes) the source.
func (h *SymbolHub) Set(s SymbolSource) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.s = s
	h.mu.Unlock()
}

// Get returns the source, or nil.
func (h *SymbolHub) Get() SymbolSource {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.s
}

// SymbolHub returns the hub the registry's Outline and Symbol tools consult,
// or nil for a registry built without them.
func (r *Registry) SymbolHub() *SymbolHub {
	if r == nil {
		return nil
	}
	return r.symbols
}

// symbolUnavailable is the whole answer when the index is not there. It is the
// one place a result points the model at Grep: a withheld file's rows never do.
const symbolUnavailable = "[symbol index unavailable for this call: use Grep to search for the name]"

// Outline lists the declarations of one file.
type Outline struct {
	Root string
	Cwd  *Cwd
	Hub  *SymbolHub
}

// Definition returns the static tool metadata.
func (*Outline) Definition() Definition {
	return Definition{
		Name: OutlineName,
		Description: "List the declarations in one file: kind, name, receiver or container and line span, in source order, without reading the file. " +
			"Use it to decide which part of a large file to Read with offset and limit. " +
			"Only files in the working directory that are indexed are listed; ignored, hidden, binary, oversized and credential files are not.",
		Properties: map[string]Property{
			"file_path": {Type: "string", Format: FormatPath, Description: "Path to the file: relative to the working directory, or an absolute path under the session root"},
		},
		Required: []string{"file_path"},
	}
}

// Kind returns KindSymbol.
func (*Outline) Kind() Kind { return KindSymbol }

// Subject returns the permission-rule subject: the path argument resolved
// through the working directory, as Read's is.
func (t *Outline) Subject(args map[string]any) string {
	if s, ok := argString(args, "file_path"); ok {
		return subjectPath(t.Root, t.Cwd, s)
	}
	return ""
}

// Mutates reports false.
func (*Outline) Mutates() bool { return false }

// Execute lists the file's declarations.
func (t *Outline) Execute(ctx context.Context, args map[string]any) (Result, error) {
	raw, ok := argString(args, "file_path")
	if !ok || strings.TrimSpace(raw) == "" {
		return Result{}, fmt.Errorf("missing file_path argument")
	}
	src := t.Hub.Get()
	if src == nil {
		return Result{Kind: KindSymbol, Content: "[the symbol index is not available in this session]"}, nil
	}
	res, err := resolvePath(t.Root, t.Cwd, raw)
	if err != nil {
		return Result{}, err
	}
	if !isPrimary(res.Root, t.Cwd) {
		return Result{}, fmt.Errorf("%s is outside the session root, which is all the symbol index covers; Read it instead", sanitize.Line(raw, 200))
	}
	rel := strings.ReplaceAll(res.Rel, "\\", "/")
	ans := src.Outline(ctx, rel)
	switch {
	case ans.Unavailable:
		return Result{Kind: KindSymbol, Content: symbolUnavailable}, nil
	case ans.NotIndexed && len(ans.Withheld) == 0:
		return Result{Kind: KindSymbol, Content: "[" + sanitize.Line(rel, 200) + " is not in the symbol index: it may be ignored, hidden, binary, oversized, credential-bearing, data or prose rather than source, or beyond the index's file bound]"}, nil
	}
	var b strings.Builder
	rows := cleanRows(ans.Rows)
	if len(rows) > 0 {
		fmt.Fprintf(&b, "%s: %d declaration(s)\n", sanitize.Line(rel, 200), len(rows))
		for _, r := range rows {
			fmt.Fprintf(&b, "%s  %s\n", span(r), label(r))
		}
	} else if len(ans.Withheld) == 0 {
		fmt.Fprintf(&b, "%s: no declarations found\n", sanitize.Line(rel, 200))
	}
	writeWithheld(&b, ans.Withheld)
	return Result{Kind: KindSymbol, Content: strings.TrimRight(b.String(), "\n")}, nil
}

// Symbol finds where a name is declared.
type Symbol struct {
	Hub *SymbolHub
	// MaxResults is the most rows one call returns; the user's
	// symbol_index.max_results.
	MaxResults int
}

// Definition returns the static tool metadata.
func (*Symbol) Definition() Definition {
	return Definition{
		Name: SymbolName,
		Description: "Find where a function, method, type, constant or variable is declared in the working directory, without reading files. " +
			"Returns path:line, kind and receiver, best match first: an exact name, then the same name in another case, then names that start with it, then names that contain it. " +
			"Write Type.Method for a method. Use it to jump to a definition, then Read that range.",
		Properties: map[string]Property{
			"name":  {Type: "string", Description: "A declared name, or Type.Method (letters, digits and underscore)"},
			"kind":  {Type: "string", Enum: locate.Kinds, Description: "Optional: keep only this kind of declaration"},
			"limit": {Type: "integer", Description: "Optional: how many rows to return at most (the default is the configured maximum)"},
		},
		Required: []string{"name"},
	}
}

// Kind returns KindSymbol.
func (*Symbol) Kind() Kind { return KindSymbol }

// Subject returns the name asked for.
func (*Symbol) Subject(args map[string]any) string {
	v, _ := argString(args, "name")
	return sanitize.Ident(v, symbolNameMax)
}

// Mutates reports false.
func (*Symbol) Mutates() bool { return false }

// validSymbolQuery reports whether s is a name or Type.Method: ASCII letters,
// digits and underscore with at most one dot between two non-empty parts.
func validSymbolQuery(s string) bool {
	if s == "" || len(s) > symbolNameMax || strings.Count(s, ".") > 1 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= '0' && c <= '9') {
				return false
			}
		}
	}
	return true
}

// Execute finds the declarations.
func (t *Symbol) Execute(ctx context.Context, args map[string]any) (Result, error) {
	name, _ := argString(args, "name")
	name = strings.TrimSpace(name)
	if name == "" {
		return Result{}, fmt.Errorf("missing name argument")
	}
	if !validSymbolQuery(name) {
		return Result{}, fmt.Errorf("name must be a declared name or Type.Method: letters, digits and underscore, at most %d bytes", symbolNameMax)
	}
	kind, _ := argString(args, "kind")
	kind = strings.TrimSpace(kind)
	if kind != "" && !locate.ValidKind(kind) {
		return Result{}, fmt.Errorf("kind must be one of %s", strings.Join(locate.Kinds, ", "))
	}
	limit := t.MaxResults
	if limit <= 0 {
		limit = 50
	}
	if n, ok := argInt64(args, "limit"); ok && n > 0 {
		limit = int(min(n, int64(limit)))
	}
	src := t.Hub.Get()
	if src == nil {
		return Result{Kind: KindSymbol, Content: "[the symbol index is not available in this session]"}, nil
	}
	ans := src.Lookup(ctx, name, kind, limit)
	if ans.Unavailable {
		return Result{Kind: KindSymbol, Content: symbolUnavailable}, nil
	}
	rows := cleanRows(ans.Rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	var b strings.Builder
	if len(rows) == 0 {
		if len(ans.Withheld) == 0 {
			fmt.Fprintf(&b, "no declaration of %q in the index\n", sanitize.Ident(name, symbolNameMax))
		}
	} else {
		fmt.Fprintf(&b, "%d declaration(s) matching %q:\n", len(rows), sanitize.Ident(name, symbolNameMax))
		for _, r := range rows {
			fmt.Fprintf(&b, "%s:%s  %s\n", sanitize.Line(r.Path, 300), span(r), label(r))
		}
	}
	writeWithheld(&b, ans.Withheld)
	if ans.Truncated {
		fmt.Fprintf(&b, "(the index covers %d of %d source files, so a name in the rest is not found here)\n", ans.Files, ans.Eligible)
	}
	return Result{Kind: KindSymbol, Content: strings.TrimRight(b.String(), "\n")}, nil
}

// cleanRows passes every row through the sink for a declared name: kinds
// outside the closed set are blanked, names and containers are reduced to
// identifier characters, and a row left without a name or with a path that
// fails the lexical check is dropped.
func cleanRows(in []SymbolRow) []SymbolRow {
	out := make([]SymbolRow, 0, len(in))
	for _, r := range in {
		r.Name = sanitize.Ident(r.Name, symbolNameMax)
		r.Container = sanitize.Ident(r.Container, symbolNameMax)
		if r.Name == "" || r.Line < 1 {
			continue
		}
		if r.Path != "" && sanitize.PathText(r.Path) != nil {
			continue
		}
		if !locate.ValidKind(r.Kind) {
			r.Kind = ""
		}
		out = append(out, r)
	}
	return out
}

// span renders a row's line range: 12 or 12-40.
func span(r SymbolRow) string {
	if r.EndLine > r.Line {
		return fmt.Sprintf("%d-%d", r.Line, r.EndLine)
	}
	return fmt.Sprintf("%d", r.Line)
}

// label renders a row's kind and qualified name.
func label(r SymbolRow) string {
	name := r.Name
	if r.Container != "" {
		name = r.Container + "." + name
	}
	if r.Kind == "" {
		return name
	}
	return r.Kind + " " + name
}

// writeWithheld names the files whose rows were left out. The line states the
// fact and points at no other tool: the file's content stays withheld.
func writeWithheld(b *strings.Builder, w []SymbolWithheld) {
	sort.Slice(w, func(i, j int) bool { return w[i].Path < w[j].Path })
	for _, x := range w {
		fmt.Fprintf(b, "[%d declaration(s) from %s withheld: an earlier read of this file was classified %s]\n",
			x.Rows, sanitize.Line(x.Path, 300), sanitize.Line(x.Verdict, 60))
	}
}

var (
	_ Tool    = (*Outline)(nil)
	_ Mutator = (*Outline)(nil)
	_ Tool    = (*Symbol)(nil)
	_ Mutator = (*Symbol)(nil)
)
