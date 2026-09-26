package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/proc"
)

// Grep searches files under Root for a pattern, preferring ripgrep and falling
// back to POSIX grep. Both backends are normalised to one output shape:
// path:line:text.
//
// When an added workspace root is given as the path, the search runs inside
// that root and results are returned as absolute paths so they can be handed
// straight back to Read.
type Grep struct {
	Cwd        *Cwd
	Root       string
	MaxMatches int
	MaxLineLen int
	rgPath     string // cached; "" when rg is unavailable
	// noRg forces the POSIX grep backend, so tests cover the fallback on a
	// host that has ripgrep.
	noRg bool
}

// Grep output modes, named as the trained harnesses name them.
const (
	grepModeContent = "content"
	grepModeFiles   = "files_with_matches"
	grepModeCount   = "count"
)

// grepMaxHeadLimit bounds head_limit: a model may widen the default cap, but
// not without limit.
const grepMaxHeadLimit = 1000

// grepMaxContext bounds -A/-B/-C so a context request cannot turn a search
// into a whole-file dump.
const grepMaxContext = 20

// grepTypeGlobs maps the common rg --type names to file globs for the POSIX
// grep fallback, which has no type table. An unknown type on the fallback is
// an error rather than a silently unfiltered search.
var grepTypeGlobs = map[string][]string{
	"go": {"*.go"}, "py": {"*.py"}, "js": {"*.js", "*.mjs", "*.cjs", "*.jsx"},
	"ts": {"*.ts", "*.tsx"}, "rust": {"*.rs"}, "java": {"*.java"}, "c": {"*.c", "*.h"},
	"cpp": {"*.cpp", "*.cc", "*.cxx", "*.hpp", "*.hh", "*.h"}, "rb": {"*.rb"},
	"php": {"*.php"}, "sh": {"*.sh", "*.bash"}, "md": {"*.md", "*.markdown"},
	"json": {"*.json"}, "yaml": {"*.yaml", "*.yml"}, "toml": {"*.toml"},
	"html": {"*.html", "*.htm"}, "css": {"*.css"}, "sql": {"*.sql"},
	"kotlin": {"*.kt", "*.kts"}, "swift": {"*.swift"}, "cs": {"*.cs"},
}

// Definition returns the static tool metadata.
func (g *Grep) Definition() Definition {
	return Definition{
		Name: "Grep",
		Description: "Search file contents under the working directory for a regular expression. " +
			"output_mode \"content\" (the default here — Claude Code defaults to files_with_matches; pass it explicitly for a file list) returns matching lines as `path:line:text`, with -A/-B/-C context lines as `path-line-text`; \"files_with_matches\" returns matching paths; \"count\" returns `path:count`. " +
			"Results are sorted by path then line number, long lines are clipped, and the total is capped (200 entries unless head_limit says otherwise) — a broad pattern is truncated, so narrow it with glob, type or path rather than paging. " +
			"The search is recursive from `path` (or the working directory) and confined to it. " +
			"Searches inside an added workspace directory return absolute paths; searches inside the session root return root-relative paths. " +
			"Use Grep to find where something is written; use Glob to find files by name.",
		Properties: map[string]Property{
			"pattern":     {Type: "string", Description: "The regular expression to search for; a literal string is also a valid pattern"},
			"path":        {Type: "string", Description: "Optional subdirectory or single file to search: an absolute filesystem path under one of the session roots, or relative to the working directory; a leading `/` not under any root is relative to the session root. Defaults to the working directory."},
			"glob":        {Type: "string", Description: "Only search files matching this glob, e.g. \"*.go\" or \"**/*_test.go\""},
			"type":        {Type: "string", Description: "Only search files of this type, e.g. go, py, js, ts, rust, java"},
			"-i":          {Type: "boolean", Description: "Case-insensitive search"},
			"-n":          {Type: "boolean", Description: "Show line numbers in content mode (default true)"},
			"-A":          {Type: "integer", Description: "Lines of context after each match (content mode)"},
			"-B":          {Type: "integer", Description: "Lines of context before each match (content mode)"},
			"-C":          {Type: "integer", Description: "Lines of context before and after each match (content mode)"},
			"output_mode": {Type: "string", Description: "content (default), files_with_matches, or count", Enum: []string{grepModeContent, grepModeFiles, grepModeCount}},
			"head_limit":  {Type: "integer", Description: "Return at most this many lines or entries (default 200, max 1000)"},
		},
		Required: []string{"pattern"},
	}
}

// Kind returns "grep".
func (g *Grep) Kind() Kind { return KindGrep }

// Subject returns the pattern for permission evaluation.
func (g *Grep) Subject(args map[string]any) string {
	if s, ok := args["pattern"].(string); ok {
		return s
	}
	return ""
}

// grepQuery is one parsed Grep call.
type grepQuery struct {
	pattern      string
	glob         string
	fileType     string
	ignoreCase   bool
	lineNumbers  bool
	after        int
	before       int
	mode         string
	limit        int
	searchTarget string // path argument handed to the backend; "" means the run dir
}

func (q grepQuery) hasContext() bool { return q.after > 0 || q.before > 0 }

// parseGrepQuery validates and bounds the optional arguments.
func (g *Grep) parseGrepQuery(args map[string]any) (grepQuery, error) {
	q := grepQuery{lineNumbers: true, mode: grepModeContent}
	pattern, ok := args["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return q, fmt.Errorf("missing pattern argument")
	}
	q.pattern = pattern
	q.glob, _ = argString(args, "glob")
	q.fileType, _ = argString(args, "type")
	q.ignoreCase, _ = argBool(args, "-i")
	if n, ok := argBool(args, "-n"); ok {
		q.lineNumbers = n
	}
	ctxArg := func(key string) (int, error) {
		v, ok := argInt64(args, key)
		if !ok {
			return 0, nil
		}
		if v < 0 {
			return 0, fmt.Errorf("%s must not be negative", key)
		}
		return int(min(v, grepMaxContext)), nil
	}
	c, err := ctxArg("-C")
	if err != nil {
		return q, err
	}
	q.after, q.before = c, c
	if a, err := ctxArg("-A"); err != nil {
		return q, err
	} else if a > 0 {
		q.after = a
	}
	if b, err := ctxArg("-B"); err != nil {
		return q, err
	} else if b > 0 {
		q.before = b
	}
	if m, ok := argString(args, "output_mode"); ok && m != "" {
		switch m {
		case grepModeContent, grepModeFiles, grepModeCount:
			q.mode = m
		default:
			return q, fmt.Errorf("output_mode must be content, files_with_matches or count, not %q", m)
		}
	}
	q.limit = g.MaxMatches
	if q.limit <= 0 {
		q.limit = 200
	}
	if hl, ok := argInt64(args, "head_limit"); ok {
		if hl <= 0 {
			return q, fmt.Errorf("head_limit must be positive")
		}
		q.limit = int(min(hl, grepMaxHeadLimit))
	}
	return q, nil
}

// Execute searches and returns normalised, sorted, bounded matches.
func (g *Grep) Execute(ctx context.Context, args map[string]any) (Result, error) {
	q, err := g.parseGrepQuery(args)
	if err != nil {
		return Result{}, err
	}
	root := g.Root
	sub := ""
	if s, ok := args["path"].(string); ok && s != "" {
		res, err := resolvePath(g.Root, g.Cwd, s)
		if err != nil {
			return Result{}, err
		}
		root = res.Root
		sub = res.Rel
	} else {
		// No path argument means "here", and "here" is the working directory.
		// The search still runs from the primary root so the reported paths
		// stay root-relative and can be handed straight back to Read.
		sub = g.Cwd.Rel()
	}
	q.searchTarget = sub
	if g.rgPath == "" && !g.noRg {
		g.rgPath, _ = exec.LookPath("rg")
	}
	backend := "grep"
	var out []byte
	if g.rgPath != "" && !g.noRg {
		backend = "rg"
		out, _ = g.runRg(ctx, q, root)
	} else {
		argv, err := grepArgs(q)
		if err != nil {
			return Result{}, err
		}
		out, _ = g.runGrep(ctx, argv, root)
	}
	maxLineLen := g.MaxLineLen
	if maxLineLen <= 0 {
		maxLineLen = 200
	}
	var lines []string
	switch {
	case q.mode == grepModeContent && q.hasContext():
		// Context output is already grouped per file and in line order; a
		// re-sort would scatter each match's context.
		lines = boundGrepLines(out, maxLineLen, q.limit, root, g.Root)
	case q.mode == grepModeCount:
		lines = normaliseGrepLines(dropZeroCounts(out), maxLineLen, q.limit, root, g.Root)
	default:
		lines = normaliseGrepLines(out, maxLineLen, q.limit, root, g.Root)
	}
	return GrepResult(strings.Join(lines, "\n"), backend), nil
}

// rgArgs renders a query as ripgrep flags.
func rgArgs(q grepQuery) []string {
	args := []string{"--no-heading", "--color=never"}
	switch q.mode {
	case grepModeFiles:
		args = append(args, "--files-with-matches")
	case grepModeCount:
		args = append(args, "--count")
	default:
		if q.lineNumbers {
			args = append(args, "--line-number")
		} else {
			args = append(args, "--no-line-number")
		}
		if q.hasContext() {
			// Sorted output keeps each file's context blocks together and in
			// order, so no re-sort is needed afterwards.
			args = append(args, "--sort", "path")
			if q.after > 0 {
				args = append(args, "-A", strconv.Itoa(q.after))
			}
			if q.before > 0 {
				args = append(args, "-B", strconv.Itoa(q.before))
			}
		}
	}
	if q.ignoreCase {
		args = append(args, "--ignore-case")
	}
	if q.glob != "" {
		args = append(args, "--glob", q.glob)
	}
	if q.fileType != "" {
		args = append(args, "--type", q.fileType)
	}
	args = append(args, "--", q.pattern)
	if q.searchTarget != "" {
		args = append(args, q.searchTarget)
	}
	return args
}

// grepArgs renders a query as POSIX/GNU grep flags.
func grepArgs(q grepQuery) ([]string, error) {
	args := []string{"-r"}
	switch q.mode {
	case grepModeFiles:
		args = append(args, "-l")
	case grepModeCount:
		args = append(args, "-c")
	default:
		if q.lineNumbers {
			args = append(args, "-n")
		}
		if q.after > 0 {
			args = append(args, "-A", strconv.Itoa(q.after))
		}
		if q.before > 0 {
			args = append(args, "-B", strconv.Itoa(q.before))
		}
	}
	if q.ignoreCase {
		args = append(args, "-i")
	}
	if q.glob != "" {
		args = append(args, "--include="+filepath.Base(q.glob))
	}
	if q.fileType != "" {
		globs, ok := grepTypeGlobs[q.fileType]
		if !ok {
			return nil, fmt.Errorf("unknown file type %q (ripgrep is not installed, so only common types are recognised); use glob instead", q.fileType)
		}
		for _, gl := range globs {
			args = append(args, "--include="+gl)
		}
	}
	args = append(args, "--", q.pattern)
	if q.searchTarget != "" {
		args = append(args, q.searchTarget)
	}
	return args, nil
}

func (g *Grep) runRg(ctx context.Context, q grepQuery, dir string) ([]byte, error) {
	ec := exec.CommandContext(ctx, g.rgPath, rgArgs(q)...)
	ec.Dir = dir
	ec.Env = proc.ScrubbedEnv()
	ec.Env = append(ec.Env, calltrace.Env(ctx)...)
	return ec.Output()
}

func (g *Grep) runGrep(ctx context.Context, args []string, dir string) ([]byte, error) {
	ec := exec.CommandContext(ctx, "grep", args...)
	ec.Dir = dir
	ec.Env = proc.ScrubbedEnv()
	ec.Env = append(ec.Env, calltrace.Env(ctx)...)
	return ec.Output()
}

// dropZeroCounts removes the "path:0" rows POSIX grep -c prints for every file
// it searched, so count mode reports only files that matched, as rg does.
func dropZeroCounts(out []byte) []byte {
	var kept []string
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if ln == "" || strings.HasSuffix(ln, ":0") {
			continue
		}
		kept = append(kept, ln)
	}
	if len(kept) == 0 {
		return nil
	}
	return []byte(strings.Join(kept, "\n") + "\n")
}

// boundGrepLines is normaliseGrepLines without the re-sort, for output whose
// order is already meaningful (context blocks separated by "--").
func boundGrepLines(out []byte, maxLineLen, maxLines int, matchRoot, primaryRoot string) []string {
	if len(out) == 0 {
		return nil
	}
	extra := matchRoot != primaryRoot
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.ContainsRune(ln, '\x00') {
			continue
		}
		if len(ln) > maxLineLen {
			ln = ln[:maxLineLen] + "…"
		}
		if extra && ln != "--" {
			ln = makeGrepPathAbsolute(ln, matchRoot)
		}
		lines = append(lines, ln)
		if len(lines) >= maxLines {
			break
		}
	}
	return lines
}

// normaliseGrepLines sorts path:line:text lines, caps line length, and bounds
// the total number of matches. When the match came from an extra workspace
// root, the path is made absolute so it can be handed straight back to Read.
func normaliseGrepLines(out []byte, maxLineLen, maxMatches int, matchRoot, primaryRoot string) []string {
	if len(out) == 0 {
		return nil
	}
	raw := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	lines := make([]string, 0, len(raw))
	extra := matchRoot != primaryRoot
	for _, ln := range raw {
		if ln == "" || strings.ContainsRune(ln, '\x00') {
			continue
		}
		if len(ln) > maxLineLen {
			ln = ln[:maxLineLen] + "…"
		}
		if extra {
			ln = makeGrepPathAbsolute(ln, matchRoot)
		}
		lines = append(lines, ln)
	}
	sort.SliceStable(lines, func(i, j int) bool {
		return grepKey(lines[i]) < grepKey(lines[j])
	})
	if len(lines) > maxMatches {
		lines = lines[:maxMatches]
	}
	return lines
}

// makeGrepPathAbsolute prefixes the leading path of a grep result line with
// an absolute root when the match came from an extra workspace root.
func makeGrepPathAbsolute(line, root string) string {
	c1 := strings.Index(line, ":")
	if c1 < 0 {
		return line
	}
	p := line[:c1]
	if filepath.IsAbs(p) {
		return line
	}
	return filepath.Join(root, p) + line[c1:]
}

// grepKey returns a stable sort key: the path, then the line number.
func grepKey(line string) string {
	c1 := strings.Index(line, ":")
	if c1 < 0 {
		return line
	}
	c2 := strings.Index(line[c1+1:], ":")
	if c2 < 0 {
		return line
	}
	num := line[c1+1 : c1+1+c2]
	if n, err := strconv.Atoi(num); err == nil {
		return line[:c1] + ":" + fmt.Sprintf("%08d", n)
	}
	return line
}

// GrepResult constructs a Grep tool result.
func GrepResult(content, backend string) Result {
	return Result{Kind: KindGrep, Content: content, Meta: map[string]any{"backend": backend}}
}
