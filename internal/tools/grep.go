package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
}

// Definition returns the static tool metadata.
func (g *Grep) Definition() Definition {
	return Definition{
		Name: "Grep",
		Description: "Search file contents under the working directory for a regular expression. " +
			"Returns matching lines as `path:line:text`, sorted by path then line number, with long lines clipped and the total capped (200 matches by default) — a broad pattern is silently truncated, so narrow it rather than paging. " +
			"The search is recursive from `path` (or the working directory) and confined to it. " +
			"Searches inside an added workspace directory return absolute paths; searches inside the session root return root-relative paths. " +
			"Use Grep to find where something is written; use Glob to find files by name.",
		Properties: map[string]Property{
			"pattern": {Type: "string", Description: "The regular expression to search for; a literal string is also a valid pattern"},
			"path":    {Type: "string", Description: "Optional subdirectory or single file to search, relative to the working directory, or an absolute path inside an added workspace root. Defaults to the working directory."},
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

// Execute searches and returns normalised, sorted, bounded matches.
func (g *Grep) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return Result{}, fmt.Errorf("missing pattern argument")
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
	if g.rgPath == "" {
		g.rgPath, _ = exec.LookPath("rg")
	}
	backend := "grep"
	var out []byte
	if g.rgPath != "" {
		backend = "rg"
		out, _ = g.runRg(ctx, pattern, sub, root)
	} else {
		out, _ = g.runGrep(ctx, pattern, sub, root)
	}
	maxMatches := g.MaxMatches
	if maxMatches <= 0 {
		maxMatches = 200
	}
	maxLineLen := g.MaxLineLen
	if maxLineLen <= 0 {
		maxLineLen = 200
	}
	lines := normaliseGrepLines(out, maxLineLen, maxMatches, root, g.Root)
	return GrepResult(strings.Join(lines, "\n"), backend), nil
}

func (g *Grep) runRg(ctx context.Context, pattern, sub, dir string) ([]byte, error) {
	args := []string{"--no-heading", "--line-number", "--color=never", "--", pattern}
	if sub != "" {
		args = append(args, sub)
	}
	ec := exec.CommandContext(ctx, g.rgPath, args...)
	ec.Dir = dir
	ec.Env = scrubbedEnv()
	return ec.Output()
}

func (g *Grep) runGrep(ctx context.Context, pattern, sub, dir string) ([]byte, error) {
	args := []string{"-rn", "--", pattern}
	if sub != "" {
		args = append(args, sub)
	}
	ec := exec.CommandContext(ctx, "grep", args...)
	ec.Dir = dir
	ec.Env = scrubbedEnv()
	return ec.Output()
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
