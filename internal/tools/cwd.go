package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Cwd is the working directory the tools of one session share.
//
// Two directories are in play and they are not the same thing:
//
//   - the **root set** is the confinement boundary. It contains one primary
//     root plus zero or more additional workspace roots added with /add-dir.
//     Every path a tool touches must land inside at least one root or the call
//     is refused. The primary root is fixed; extra roots can be widened only
//     through an explicit /add-dir confirmation.
//   - the **working directory** is where relative paths are interpreted from.
//     It sits inside the primary root and moves within it, so that an agent
//     working in a subtree can name files the way someone standing in that
//     subtree would. A path beginning with "/" is root-relative: extras are
//     checked first (longest match), then the primary root. An absolute
//     filesystem path that lands in an added root is returned as an absolute
//     path by Glob/Grep so it can be handed back to Read unchanged.
//
// Moving the working directory is therefore not a widening of authority: the
// set of reachable files is identical before and after, only the spelling of
// a relative path changes. That is the whole reason the move is allowed at
// all.
//
// A Cwd is safe for concurrent use: read-only tools run in a concurrent
// fan-out and all of them resolve paths through it.
type Cwd struct {
	mu      sync.RWMutex
	primary string
	extra   []string // absolute, cleaned, EvalSymlinks-resolved roots
	rel     string   // slash-separated path under primary root; "" means the primary root
}

// NewCwd returns a tracker rooted at root and positioned there.
func NewCwd(root string) *Cwd {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Cwd{primary: abs}
}

// Root returns the primary confinement boundary, which never changes.
func (c *Cwd) Root() string {
	if c == nil {
		return ""
	}
	return c.primary
}

// Roots returns all confinement roots: the primary root first, followed by
// any additional workspace roots in insertion order.
func (c *Cwd) Roots() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, 1+len(c.extra))
	out = append(out, c.primary)
	out = append(out, c.extra...)
	return out
}

// AddRoot widens the confinement boundary to include dir. It rejects a root
// that contains or is contained by an existing root, because subsumption
// would make one path resolvable two ways.
func (c *Cwd) AddRoot(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", dir)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if abs == c.primary || strings.HasPrefix(abs, c.primary+string(filepath.Separator)) {
		return fmt.Errorf("directory already inside the primary session root")
	}
	if strings.HasPrefix(c.primary, abs+string(filepath.Separator)) {
		return fmt.Errorf("primary session root is inside the requested directory")
	}
	for _, r := range c.extra {
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) || strings.HasPrefix(r, abs+string(filepath.Separator)) {
			return fmt.Errorf("directory overlaps an existing workspace root")
		}
	}
	c.extra = append(c.extra, abs)
	return nil
}

// expandHome resolves a leading "~/" against the user's home directory so a
// path like ~/src/signet/README.md is treated as the absolute filesystem path
// the model meant, rather than as a literal "~" path segment.
func expandHome(raw string) string {
	if raw != "~" && !strings.HasPrefix(raw, "~/") {
		return raw
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return raw
	}
	if raw == "~" {
		return home
	}
	return filepath.Join(home, raw[2:])
}

// rootFor reports whether raw (which must begin with "/", or with "~/" which
// is expanded first) lands under one of the confinement roots — the primary
// root or an extra root. Roots are checked longest-match first so a nested
// added directory resolves to the most specific root.
func (c *Cwd) rootFor(raw string) (root, rest string, ok bool) {
	raw = expandHome(raw)
	if !strings.HasPrefix(raw, "/") {
		return "", "", false
	}
	abs := filepath.Clean(raw)
	c.mu.RLock()
	defer c.mu.RUnlock()
	candidates := append([]string{c.primary}, c.extra...)
	candidates = sortedRoots(candidates)
	var best string
	for _, r := range candidates {
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) {
			best = r
			break
		}
	}
	if best == "" {
		return "", "", false
	}
	rest = strings.TrimPrefix(abs, best)
	rest = strings.TrimPrefix(rest, string(filepath.Separator))
	return best, filepath.ToSlash(rest), true
}

// Rel returns the working directory relative to the primary root, slash-separated.
// "" means the working directory is the primary root.
func (c *Cwd) Rel() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rel
}

// Dir returns the absolute working directory (always inside the primary root).
func (c *Cwd) Dir() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.rel == "" {
		return c.primary
	}
	return filepath.Join(c.primary, filepath.FromSlash(c.rel))
}

// Change moves the working directory to raw, resolved by the same rule as
// every other path argument, and returns the new root-relative location. The
// target must exist and be a directory inside the primary root; anything else
// leaves the working directory where it was, because a half-applied move
// would make every following relative path mean something the model did not
// intend.
func (c *Cwd) Change(raw string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no working directory tracker")
	}
	rel, err := c.resolveDir(raw)
	if err != nil {
		return "", err
	}
	abs := c.primary
	if rel != "" {
		abs = filepath.Join(c.primary, filepath.FromSlash(rel))
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", raw)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.rel = rel
	return rel, nil
}

// resolveDir applies the resolution rule to a directory argument and confines
// the result to the primary root.
func (c *Cwd) resolveDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("missing path argument")
	}
	joined := c.join(raw)
	rel, err := SanitizePath(c.primary, joined)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// join applies the resolution rule without confining: a leading "/" is
// checked against extra roots first, and if it does not land in one it is
// treated as relative to the primary root. Any other path is relative to the
// working directory.
func (c *Cwd) join(raw string) string {
	raw = expandHome(raw)
	if strings.HasPrefix(raw, "/") {
		if _, rest, ok := c.rootFor(raw); ok {
			return rest
		}
		return strings.TrimPrefix(raw, "/")
	}
	c.mu.RLock()
	rel := c.rel
	c.mu.RUnlock()
	if rel == "" {
		return raw
	}
	return filepath.Join(filepath.FromSlash(rel), raw)
}

// resolved is the result of resolving a path argument: the root it landed in
// and the clean path relative to that root.
type resolved struct {
	Root string // confinement root the path landed in
	Rel  string // clean path relative to Root
}

// Abs returns the absolute filesystem path.
func (r resolved) Abs() string { return filepath.Join(r.Root, r.Rel) }

// resolvePath resolves a tool's path argument.
func resolvePath(root string, cwd *Cwd, raw string) (resolved, error) {
	if cwd == nil {
		rel, err := SanitizePath(root, raw)
		if err != nil {
			return resolved{}, err
		}
		return resolved{Root: root, Rel: rel}, nil
	}
	if root, rest, ok := cwd.rootFor(raw); ok {
		rel, err := SanitizePath(root, rest)
		if err != nil {
			return resolved{}, err
		}
		return resolved{Root: root, Rel: rel}, nil
	}
	rel, err := SanitizePath(root, cwd.join(raw))
	if err != nil {
		return resolved{}, err
	}
	return resolved{Root: root, Rel: rel}, nil
}

// resolveNewPath is resolvePath for a path that need not exist yet.
func resolveNewPath(root string, cwd *Cwd, raw string) (resolved, error) {
	if cwd == nil {
		rel, err := SanitizeNewPath(root, raw)
		if err != nil {
			return resolved{}, err
		}
		return resolved{Root: root, Rel: rel}, nil
	}
	if root, rest, ok := cwd.rootFor(raw); ok {
		rel, err := SanitizeNewPath(root, rest)
		if err != nil {
			return resolved{}, err
		}
		return resolved{Root: root, Rel: rel}, nil
	}
	rel, err := SanitizeNewPath(root, cwd.join(raw))
	if err != nil {
		return resolved{}, err
	}
	return resolved{Root: root, Rel: rel}, nil
}

// baseDir returns the directory a subprocess should run in: the working
// directory when one is tracked, the primary root otherwise. Bash always runs
// in the primary workspace even when extra roots exist.
func baseDir(root string, cwd *Cwd) string {
	if cwd == nil {
		return root
	}
	return cwd.Dir()
}

// hasRoot reports whether root is the primary root of cwd (or the fallback
// root when cwd is nil).
func isPrimary(root string, cwd *Cwd) bool {
	if cwd == nil {
		return true
	}
	return root == cwd.primary
}

// subjectPath returns the permission-rule subject for a path argument.
// For an extra root it returns the absolute path so a permission rule can
// name it without colliding with a primary-root-relative rule. For the
// primary root it returns the root-relative path.
func subjectPath(root string, cwd *Cwd, raw string) string {
	if raw == "" {
		return ""
	}
	if cwd == nil {
		return SubjectPath(root, raw)
	}
	if r, rest, ok := cwd.rootFor(raw); ok {
		if r == cwd.primary {
			return SubjectPath(r, rest)
		}
		return filepath.Join(r, filepath.FromSlash(rest))
	}
	return SubjectPath(root, cwd.join(raw))
}

// Cd moves the session working directory. It reads nothing and writes
// nothing, so it is read-only by construction and survives both the read-only
// master switch and plan mode — an agent that may only look still needs to be
// able to look somewhere else.
type Cd struct {
	Cwd *Cwd
}

// Definition returns the static tool metadata.
func (c *Cd) Definition() Definition {
	return Definition{
		Name: "Cd",
		Description: "Change the working directory that later tool calls resolve their relative paths against. " +
			"It does not read or change any file; it only moves where \"here\" is, so a subtree can be worked in with short paths. " +
			"The target must be an existing directory inside the session root: the set of reachable files is the same before and after, so this cannot reach anything a path argument could not already reach. " +
			"A path beginning with `/` is interpreted relative to the session root, not the filesystem root; any other path is relative to the current working directory, and `..` moves up. " +
			"Passing `/` returns to the session root. The result names the new working directory.",
		Properties: map[string]Property{
			"path": {Type: "string", Description: `The directory to move to: relative to the current working directory ("internal/tools", ""), or relative to the session root when it starts with "/" ("/internal")`},
		},
		Required: []string{"path"},
	}
}

// Kind returns the native read-only kind: Cd runs no command and touches no
// file, so it belongs with the tools that only observe.
func (c *Cd) Kind() Kind { return KindNative }

// Subject returns the permission-rule subject (the requested path).
func (c *Cd) Subject(args map[string]any) string {
	s, _ := argString(args, "path")
	return s
}

// Execute moves the working directory and reports where it landed.
func (c *Cd) Execute(_ context.Context, args map[string]any) (Result, error) {
	raw, ok := argString(args, "path")
	if !ok || strings.TrimSpace(raw) == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	if c.Cwd == nil {
		return Result{}, fmt.Errorf("working directory tracking is not enabled")
	}
	rel, err := c.Cwd.Change(raw)
	if err != nil {
		return Result{}, err
	}
	if rel == "" {
		return NativeResult("working directory: / (session root)"), nil
	}
	return NativeResult("working directory: /" + rel), nil
}

// sortedRoots returns extra roots sorted longest-first without mutating the
// insertion-order slice stored on Cwd.
func sortedRoots(roots []string) []string {
	out := append([]string(nil), roots...)
	sort.Slice(out, func(i, j int) bool {
		return len(out[i]) > len(out[j])
	})
	return out
}
