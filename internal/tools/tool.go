package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/permissions"
)

// Property is a JSON-schema property for a tool definition.
type Property struct {
	Type        string              `json:"type"`
	Description string              `json:"description,omitempty"`
	Items       *Property           `json:"items,omitempty"`
	Properties  map[string]Property `json:"properties,omitempty"`
	Required    []string            `json:"required,omitempty"`
}

// Definition is the static metadata exposed to the model for a tool.
type Definition struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Properties  map[string]Property `json:"properties,omitempty"`
	Required    []string            `json:"required,omitempty"`
}

// Tool is the shared interface for all executable tools.
type Tool interface {
	Definition() Definition
	Kind() Kind
	// Subject returns the permission-rule subject (path, URL, etc.) from args.
	Subject(args map[string]any) string
	Execute(ctx context.Context, args map[string]any) (Result, error)
}

// Registry holds an ordered list of registered tools.
type Registry struct {
	tools []Tool
	// cwd is the working directory the registry's tools share. It is carried
	// on the registry rather than passed around because every narrowing
	// (ReadOnly, Plan, an agent profile's allowlist) produces a new registry
	// over the same tools, and all of them must keep pointing at the same
	// tracker — two trackers would mean two answers to "where am I".
	cwd *Cwd
}

// NewRegistry builds a registry from the provided tools.
func NewRegistry(tools ...Tool) *Registry {
	return &Registry{tools: tools}
}

// Cwd returns the shared working-directory tracker, or nil when the registry
// was built without one.
func (r *Registry) Cwd() *Cwd {
	if r == nil {
		return nil
	}
	return r.cwd
}

// withCwd returns a registry carrying the same tracker as r.
func (r *Registry) withCwd(reg *Registry) *Registry {
	reg.cwd = r.cwd
	return reg
}

// Names returns every registered tool name.
func (r *Registry) Names() []string {
	out := make([]string, len(r.tools))
	for i, t := range r.tools {
		out[i] = t.Definition().Name
	}
	return out
}

// Definitions returns the wire definitions for every registered tool.
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, len(r.tools))
	for i, t := range r.tools {
		out[i] = t.Definition()
	}
	return out
}

// Find returns the tool with the given name, if present.
func (r *Registry) Find(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Definition().Name == name {
			return t, true
		}
	}
	return nil, false
}

// Mutator is implemented by tools that can report, per instance, whether they
// mutate the workspace. It exists because read-only Bash is still KindBash: a
// Kind-level predicate cannot tell the two Bash modes apart.
type Mutator interface{ Mutates() bool }

// Mutates reports whether a tool mutates the workspace. Tools that implement
// Mutator answer for themselves; every other tool falls back to its Kind's
// read-only classification.
func Mutates(t Tool) bool {
	if m, ok := t.(Mutator); ok {
		return m.Mutates()
	}
	return !t.Kind().ReadOnly()
}

// ReadOnly returns a registry with every mutating tool removed. It is the
// single place the read-only master switch is enforced.
func (r *Registry) ReadOnly() *Registry {
	var list []Tool
	for _, t := range r.tools {
		if !Mutates(t) {
			list = append(list, t)
		}
	}
	return r.withCwd(NewRegistry(list...))
}

// WithoutPlanOnly returns a registry that drops tools marked as plan-only so
// they are not advertised to agent-mode or goal-mode turns. Plan mode keeps
// them because they are the model's way of declaring the plan complete.
func (r *Registry) WithoutPlanOnly() *Registry {
	var list []Tool
	for _, t := range r.tools {
		if po, ok := t.(PlanOnly); ok && po.PlanOnly() {
			continue
		}
		list = append(list, t)
	}
	return r.withCwd(NewRegistry(list...))
}

// Only returns a registry holding just the named tools, in the order the
// names are given, carrying the same working-directory tracker. Unknown
// names are skipped: an allowlist naming a tool this build does not have
// narrows, it does not fail.
func (r *Registry) Only(names ...string) *Registry {
	var list []Tool
	for _, name := range names {
		if t, ok := r.Find(name); ok {
			list = append(list, t)
		}
	}
	return r.withCwd(NewRegistry(list...))
}

// PlanSurface is how far the plan-mode registry may relax. The zero value
// is the fail-closed surface: no write tools, no Bash.
type PlanSurface struct {
	GuardrailsOff bool
	Perms         permissions.Settings
}

// Plan returns the registry as plan mode offers it with the zero (fail-closed)
// surface. It exists so callers that do not need a configurable surface (most
// explore subagents and existing tests) keep working without change.
func (r *Registry) Plan() *Registry { return r.PlanWith(PlanSurface{}) }

// PlanWith returns the registry as plan mode offers it for the given surface.
// Guardrails off yields the full surface. Otherwise mutating tools are removed.
// When the user has written a Bash allow rule, a read-only Bash is offered;
// otherwise Bash is dropped entirely.
func (r *Registry) PlanWith(surface PlanSurface) *Registry {
	if surface.GuardrailsOff {
		return r
	}
	base := r.ReadOnly()
	if surface.Perms.HasAllowRule("Bash") {
		// Ensure a read-only Bash is advertised. The registry may have been
		// built in full mode, in which case ReadOnly removed Bash entirely.
		var hasBash bool
		for _, t := range base.tools {
			if strings.EqualFold(t.Definition().Name, "bash") {
				hasBash = true
				break
			}
		}
		if !hasBash {
			cwd := base.Cwd()
			root := ""
			if cwd != nil {
				root = cwd.Dir()
			}
			bash := &Bash{Root: root, ReadOnly: true, Timeout: 30 * time.Second, MaxBytes: 64 * 1024, Cwd: cwd}
			base = base.withCwd(NewRegistry(append(base.tools, bash)...))
		}
		return base
	}
	var list []Tool
	for _, t := range base.tools {
		if !strings.EqualFold(t.Definition().Name, "bash") {
			list = append(list, t)
		}
	}
	return base.withCwd(NewRegistry(list...))
}

// Targeter is implemented by mutating tools that can name the concrete paths
// they will touch, so the diff recorder can snapshot exactly those files even
// outside a git repository.
type Targeter interface {
	Targets(args map[string]any) []string
}

// SanitizePath resolves a user-provided path against root, follows symlinks,
// and returns the clean relative path or an error if it escapes root.
func SanitizePath(root, raw string) (string, error) {
	if strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("path contains NUL")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, raw)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return rel, nil
}

// SanitizeNewPath resolves and confines a path that may not exist yet. It
// resolves the deepest existing ancestor, re-appends the unresolved tail, and
// then resolves a final component that is itself a symlink. A path whose
// resolved form escapes root is rejected.
func SanitizeNewPath(root, raw string) (string, error) {
	if strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("path contains NUL")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, raw)
	rel, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}

	// Walk upward from the target to the deepest component that exists.
	existing := joined
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("root does not exist")
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}

	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if err := confine(absRoot, resolved); err != nil {
		return "", err
	}

	full := resolved
	if len(tail) > 0 {
		full = filepath.Join(append([]string{resolved}, tail...)...)
	}

	// If the full path already exists as a symlink (or points through one),
	// resolve it too and re-confine.
	if resolvedFinal, err := filepath.EvalSymlinks(full); err == nil {
		full = resolvedFinal
		if err := confine(absRoot, full); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	out, err := filepath.Rel(absRoot, full)
	if err != nil {
		return "", err
	}
	return out, nil
}

// SubjectPath returns a pure-lexical cleaned path relative to root, with no
// symlink resolution. It is the permission-rule subject for mutating tools, so
// a Deny rule cannot be bypassed by "./" or ".." indirection.
func SubjectPath(root, raw string) string {
	if raw == "" {
		return ""
	}
	if root == "" {
		return filepath.ToSlash(filepath.Clean(raw))
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(raw))
	}
	joined := filepath.Join(absRoot, raw)
	rel, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(raw))
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

// confine rejects a resolved absolute path that escapes absRoot.
func confine(absRoot, resolved string) error {
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes root")
	}
	return nil
}
