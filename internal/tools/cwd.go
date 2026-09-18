package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cwd is the working directory the tools of one session share.
//
// Two directories are in play and they are not the same thing:
//
//   - the **root** is the confinement boundary. It is fixed for the life of
//     the session and nothing can move it. Every path a tool touches resolves
//     inside it or the call is refused.
//   - the **working directory** is where relative paths are interpreted from.
//     It starts at the root and moves within it, so that an agent working in
//     a subtree can name files the way someone standing in that subtree
//     would.
//
// Moving the working directory is therefore not a widening of authority: the
// set of reachable files is identical before and after, only the spelling of
// a relative path changes. That is the whole reason the move is allowed at
// all.
//
// Path resolution follows one rule, which the Cd tool documents to the model:
// a path is relative to the working directory, and a path beginning with "/"
// is relative to the root. There is no third case — an absolute filesystem
// path outside the root has no spelling here, which is the point.
//
// A Cwd is safe for concurrent use: read-only tools run in a concurrent
// fan-out and all of them resolve paths through it.
type Cwd struct {
	mu   sync.RWMutex
	root string
	rel  string // slash-separated path under root; "" means the root itself
}

// NewCwd returns a tracker rooted at root and positioned there.
func NewCwd(root string) *Cwd {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Cwd{root: abs}
}

// Root returns the confinement boundary, which never changes.
func (c *Cwd) Root() string {
	if c == nil {
		return ""
	}
	return c.root
}

// Rel returns the working directory relative to the root, slash-separated.
// "" means the working directory is the root.
func (c *Cwd) Rel() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rel
}

// Dir returns the absolute working directory.
func (c *Cwd) Dir() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.rel == "" {
		return c.root
	}
	return filepath.Join(c.root, filepath.FromSlash(c.rel))
}

// Change moves the working directory to raw, resolved by the same rule as
// every other path argument, and returns the new root-relative location. The
// target must exist and be a directory; anything else leaves the working
// directory where it was, because a half-applied move would make every
// following relative path mean something the model did not intend.
func (c *Cwd) Change(raw string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no working directory tracker")
	}
	rel, err := c.resolveDir(raw)
	if err != nil {
		return "", err
	}
	abs := c.root
	if rel != "" {
		abs = filepath.Join(c.root, filepath.FromSlash(rel))
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
// the result to the root.
func (c *Cwd) resolveDir(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("missing path argument")
	}
	joined := c.join(raw)
	rel, err := SanitizePath(c.root, joined)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// join applies the resolution rule without confining: a leading "/" makes the
// path root-relative, anything else is relative to the working directory.
func (c *Cwd) join(raw string) string {
	if strings.HasPrefix(raw, "/") {
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

// resolvePath resolves a tool's path argument. A nil tracker falls back to
// resolving against root directly, which is exactly the behaviour every tool
// had before working directories were tracked — so a tool constructed without
// one is unchanged, not broken.
func resolvePath(root string, cwd *Cwd, raw string) (string, error) {
	if cwd == nil {
		return SanitizePath(root, raw)
	}
	return SanitizePath(root, cwd.join(raw))
}

// resolveNewPath is resolvePath for a path that need not exist yet.
func resolveNewPath(root string, cwd *Cwd, raw string) (string, error) {
	if cwd == nil {
		return SanitizeNewPath(root, raw)
	}
	return SanitizeNewPath(root, cwd.join(raw))
}

// baseDir returns the directory a subprocess should run in: the working
// directory when one is tracked, the root otherwise.
func baseDir(root string, cwd *Cwd) string {
	if cwd == nil {
		return root
	}
	return cwd.Dir()
}

// subjectPath returns the permission-rule subject for a path argument: the
// lexical root-relative path, resolved through the working directory first.
//
// Resolving before matching is what keeps a Deny rule honest. A rule written
// against "secrets/**" has to keep matching after a move into "secrets", and
// it only does if the subject is the path from the root rather than the path
// the model typed.
func subjectPath(root string, cwd *Cwd, raw string) string {
	if raw == "" {
		return ""
	}
	if cwd == nil {
		return SubjectPath(root, raw)
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
			"path": {Type: "string", Description: `The directory to move to: relative to the current working directory ("internal/tools", ".."), or relative to the session root when it starts with "/" ("/internal")`},
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
