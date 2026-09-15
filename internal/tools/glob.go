package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Glob lists files matching a glob pattern, preferring fd and falling back to
// an in-process filepath.WalkDir with an in-house ** matcher (no new
// dependency).
type Glob struct {
	Root       string
	MaxResults int
	fdPath     string // cached; "" when fd is unavailable
}

// Definition returns the static tool metadata.
func (g *Glob) Definition() Definition {
	return Definition{
		Name:        "Glob",
		Description: "List files matching a glob pattern (supports ** for recursion).",
		Properties: map[string]Property{
			"pattern": {Type: "string", Description: `Glob pattern, e.g. "**/*.go" or "*.md"`},
			"path":    {Type: "string", Description: "Optional base directory"},
		},
		Required: []string{"pattern"},
	}
}

// Kind returns "glob".
func (g *Glob) Kind() Kind { return KindGlob }

// Subject returns the pattern for permission evaluation.
func (g *Glob) Subject(args map[string]any) string {
	if s, ok := args["pattern"].(string); ok {
		return s
	}
	return ""
}

// Execute lists matching files, confined to Root and bounded.
func (g *Glob) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		return Result{}, fmt.Errorf("missing pattern argument")
	}
	sub := ""
	if s, ok := args["path"].(string); ok && s != "" {
		rel, err := SanitizePath(g.Root, s)
		if err != nil {
			return Result{}, err
		}
		sub = rel
	}
	max := g.MaxResults
	if max <= 0 {
		max = 200
	}
	if g.fdPath == "" {
		g.fdPath, _ = exec.LookPath("fd")
	}
	backend := "walk"
	var matches []string
	if g.fdPath != "" {
		backend = "fd"
		matches = g.runFd(ctx, pattern, sub)
	} else {
		matches = g.walk(pattern, sub)
	}
	sort.Strings(matches)
	if len(matches) > max {
		matches = matches[:max]
	}
	return GlobResult(strings.Join(matches, "\n"), backend), nil
}

func (g *Glob) runFd(ctx context.Context, pattern, sub string) []string {
	args := []string{"--type", "f", "--glob", pattern}
	if sub != "" {
		args = append(args, sub)
	} else {
		args = append(args, ".")
	}
	ec := exec.CommandContext(ctx, g.fdPath, args...)
	ec.Dir = g.Root
	ec.Env = scrubbedEnv()
	out, err := ec.Output()
	if err != nil {
		return nil
	}
	var rel []string
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if ln == "" {
			continue
		}
		r, err := filepath.Rel(g.Root, filepath.Join(g.Root, ln))
		if err != nil {
			continue
		}
		rel = append(rel, r)
	}
	return rel
}

func (g *Glob) walk(pattern, sub string) []string {
	base := filepath.Join(g.Root, sub)
	var out []string
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(g.Root, p)
		if err != nil {
			return nil
		}
		if matchGlob(pattern, filepath.ToSlash(rel)) {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// matchGlob matches a slash-separated pattern against a slash-separated path.
// ** spans zero or more segments; single segments use path.Match (*, ?, […]).
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(p, n []string) bool {
	if len(p) == 0 {
		return len(n) == 0
	}
	if p[0] == "**" {
		for i := 0; i <= len(n); i++ {
			if matchSegs(p[1:], n[i:]) {
				return true
			}
		}
		return false
	}
	if len(n) == 0 {
		return false
	}
	ok, _ := path.Match(p[0], n[0])
	if !ok {
		return false
	}
	return matchSegs(p[1:], n[1:])
}

// GlobResult constructs a Glob tool result.
func GlobResult(content, backend string) Result {
	return Result{Kind: KindGlob, Content: content, Meta: map[string]any{"backend": backend}}
}
