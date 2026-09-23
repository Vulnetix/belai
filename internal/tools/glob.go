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

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/proc"
)

// Glob lists files matching a glob pattern.
//
// Two enumerators exist — `fd` when it is installed, and an in-process
// filepath.WalkDir otherwise — but only one matcher. fd is used purely to
// enumerate candidate files; the pattern is applied by matchGlob in-process
// for both backends. That split is deliberate: fd's own --glob matches a
// pattern with no separator against the file name and rejects one that
// contains "/" outright ("The search pattern '**/*.go' contains a
// path-separation character and will not lead to any search results"), which
// silently turned every recursive pattern into an empty result. Keeping the
// matcher in-process means the two backends cannot disagree.
type Glob struct {
	Cwd        *Cwd
	Root       string
	MaxResults int
	fdPath     string // cached; "" when fd is unavailable
	fdLooked   bool   // fd lookup already attempted
}

// Definition returns the static tool metadata.
func (g *Glob) Definition() Definition {
	return Definition{
		Name: "Glob",
		Description: "Find files by path pattern under the working directory. " +
			"Returns matching file paths, one per line, relative to the working directory and sorted. " +
			"Matches whole path segments: `*` and `?` and `[…]` match within one segment, `**` spans zero or more segments. " +
			"The pattern is matched against the path relative to `path` when `path` is given, and relative to the working directory otherwise. " +
			"Searches inside an added workspace directory return absolute paths; searches inside the session root return root-relative paths. " +
			"Directories are never returned, `.git` is never searched, and the result is capped (200 paths by default). " +
			"Use Glob to locate files by name or extension; use Grep to search file contents.",
		Properties: map[string]Property{
			"pattern": {Type: "string", Description: `Glob pattern, e.g. "**/*.go" (every Go file at any depth), "*.md" (Markdown at the top level), or "internal/**/*_test.go"`},
			"path":    {Type: "string", Description: "Optional base directory to search: an absolute filesystem path under one of the session roots, or relative to the working directory; a leading `/` not under any root is relative to the session root. The pattern is then matched relative to it. Defaults to the working directory."},
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
		// Results stay relative to the primary root so a match can be handed
		// straight back to Read.
		sub = g.Cwd.Rel()
	}
	max := g.MaxResults
	if max <= 0 {
		max = 200
	}
	if !g.fdLooked {
		g.fdPath, _ = exec.LookPath("fd")
		g.fdLooked = true
	}

	backend := "walk"
	var candidates []candidate
	if g.fdPath != "" {
		backend = "fd"
		candidates = g.enumerateFd(ctx, sub, root)
	}
	// fd failing (not installed, killed, or erroring on this tree) must not
	// turn a real match into an empty answer, so the walk is the fallback in
	// every case rather than only when fd is absent.
	if candidates == nil {
		backend = "walk"
		candidates = g.enumerateWalk(sub, root)
	}

	extra := root != g.Root
	var matches []string
	for _, c := range candidates {
		if matchGlob(pattern, c.match) {
			rel := c.rel
			if extra {
				rel = c.abs
			}
			matches = append(matches, rel)
		}
	}
	sort.Strings(matches)
	if len(matches) > max {
		matches = matches[:max]
	}
	return GlobResult(strings.Join(matches, "\n"), backend), nil
}

// candidate is one enumerated file: rel is the slash path relative to the
// search root (what the model is shown), match is the slash path the pattern
// is applied to — relative to the search base, so a pattern is written against
// the directory the caller asked about rather than the repository root. abs is
// the absolute filesystem path, used when the search root is an extra
// workspace directory.
type candidate struct {
	rel   string
	match string
	abs   string
}

// enumerateFd lists every file under the search base using fd, which honours
// no ignore files here: --no-ignore and --hidden make it enumerate exactly
// what the walk enumerates, so the two backends return identical results. It
// returns nil when fd cannot answer, which makes the caller fall back.
func (g *Glob) enumerateFd(ctx context.Context, sub, root string) []candidate {
	base := filepath.Join(root, filepath.FromSlash(sub))
	// Paths are printed relative to fd's working directory and joined to base
	// here rather than asked for with --absolute-path: fd resolves its own
	// working directory, so an absolute path comes back with symlinks already
	// resolved (a temp dir under /var prints as /private/var on macOS) and is
	// no longer relative to Root.
	args := []string{
		"--type", "f",
		"--hidden", "--no-ignore",
		"--exclude", ".git",
		".",
	}
	ec := exec.CommandContext(ctx, g.fdPath, args...)
	ec.Dir = base
	ec.Env = proc.ScrubbedEnv()
	ec.Env = append(ec.Env, calltrace.Env(ctx)...)
	out, err := ec.Output()
	if err != nil {
		return nil
	}
	body := strings.TrimRight(string(out), "\n")
	if body == "" {
		// An empty tree is a legitimate answer, not a failure; return a
		// non-nil empty slice so the caller does not re-walk it.
		return []candidate{}
	}
	var list []candidate
	for _, ln := range strings.Split(body, "\n") {
		if ln == "" {
			continue
		}
		list = append(list, g.candidateFor(filepath.Join(base, filepath.FromSlash(ln)), base, root))
	}
	return list
}

// enumerateWalk lists every file under the search base in-process, skipping
// .git exactly as the fd enumerator does.
func (g *Glob) enumerateWalk(sub, root string) []candidate {
	base := filepath.Join(root, filepath.FromSlash(sub))
	list := []candidate{}
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
		list = append(list, g.candidateFor(p, base, root))
		return nil
	})
	return list
}

// candidateFor renders one absolute path as a candidate: reported relative
// to the search root, matched relative to base.
func (g *Glob) candidateFor(abs, base, root string) candidate {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		rel = abs
	}
	match := rel
	if base != root {
		if m, err := filepath.Rel(base, abs); err == nil {
			match = m
		}
	}
	return candidate{
		rel:   filepath.ToSlash(rel),
		match: filepath.ToSlash(match),
		abs:   abs,
	}
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
