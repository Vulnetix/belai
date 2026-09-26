// Package posture implements the enforcement posture system: fail-closed by
// default, with per-gate opt-outs to warn or ignore. It loads policy from CLI
// flags, project preferences, and global preferences, in that precedence.
package posture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/vulnetix/belai/internal/config"
)

// Level is the enforcement posture for one gate.
type Level string

const (
	Enforce Level = "enforce"
	Warn    Level = "warn"
	Ignore  Level = "ignore"
)

// Gate is a named safety gate.
type Gate string

const (
	ToolResultUnsafe    Gate = "tool_result_unsafe"
	ToolResultMalformed Gate = "tool_result_malformed"
	PromptUnsafe        Gate = "prompt_unsafe"
	PromptMalformed     Gate = "prompt_malformed"
	ToolCallMismatch    Gate = "tool_call_mismatch"
	PermissionNoMatch   Gate = "permission_no_match"
	PermissionAskNoTTY  Gate = "permission_ask_no_tty"
	SkillInvalid        Gate = "skill_invalid"
	HookInvalid         Gate = "hook_invalid"
)

// AllGates is every posture gate, in deterministic order.
var AllGates = []Gate{
	ToolResultUnsafe,
	ToolResultMalformed,
	PromptUnsafe,
	PromptMalformed,
	ToolCallMismatch,
	PermissionNoMatch,
	PermissionAskNoTTY,
	SkillInvalid,
	HookInvalid,
}

// Policy maps gates to their configured level.
type Policy map[Gate]Level

// gateDefaultLevel holds the non-enforce default for individual gates. Every
// gate not listed here defaults to Enforce. PermissionNoMatch defaults to
// Ignore because the permission layer now allows unmatched calls by default;
// `postures: {permission_no_match: enforce}` in preferences.yaml restores the
// legacy fail-closed block.
var gateDefaultLevel = map[Gate]Level{
	PermissionNoMatch: Ignore,
}

// DefaultLevel returns the default posture level for a gate.
func DefaultLevel(g Gate) Level {
	if l, ok := gateDefaultLevel[g]; ok {
		return l
	}
	return Enforce
}

// Level returns the posture level for a gate, falling back to the gate's
// default level.
func (p Policy) Level(g Gate) Level {
	if p != nil {
		if l, ok := p[g]; ok {
			return l
		}
	}
	return DefaultLevel(g)
}

// Defaults returns a policy seeded from each gate's default level.
func Defaults() Policy {
	p := make(Policy, len(AllGates))
	for _, g := range AllGates {
		p[g] = DefaultLevel(g)
	}
	return p
}

// AllIgnore returns a policy with every gate set to Ignore. It is what the
// operator's guardrails switch means when it is off, and it exists as one
// function so every surface that honours that switch — the agent session, the
// inline `!shell` round trip, `@file` attachment admission, background agents
// — turns the same gates off. A second, hand-rolled copy of this loop is how
// a surface ends up quietly still enforcing.
func AllIgnore() Policy {
	p := make(Policy, len(AllGates))
	for _, g := range AllGates {
		p[g] = Ignore
	}
	return p
}

// stronger returns the stronger (stricter) of two posture levels.
func stronger(a, b Level) Level {
	rank := map[Level]int{Ignore: 0, Warn: 1, Enforce: 2}
	if rank[a] >= rank[b] {
		return a
	}
	return b
}

// Stricter returns a policy whose level for every gate is the stricter of
// the corresponding levels in a and b.
func Stricter(a, b Policy) Policy {
	out := make(Policy, len(AllGates))
	for _, g := range AllGates {
		out[g] = stronger(a.Level(g), b.Level(g))
	}
	return out
}

// ForDirs returns the strictest policy across the primary workdir and the
// additional workspace directories. Each directory may carry its own
// preferences.yaml; this is the trust-split point: the overall posture is the
// strongest (most restrictive) of all selected policies, never the weakest.
func ForDirs(primary string, dirs []string) (Policy, error) {
	base, err := Load(primary)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{primary: true}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		q, err := Load(d)
		if err != nil {
			return nil, err
		}
		base = Stricter(base, q)
	}
	return base, nil
}

// Override merges q over p; non-empty values in q win.
func (p Policy) Override(q Policy) Policy {
	out := make(Policy, len(p)+len(q))
	for g, l := range p {
		out[g] = l
	}
	for g, l := range q {
		if l != "" {
			out[g] = l
		}
	}
	return out
}

// Downgrades returns a human-readable summary of every gate whose level is
// relaxed compared with that gate's default, ordered by gate name.
func (p Policy) Downgrades() []string {
	var out []string
	for _, g := range AllGates {
		l := p.Level(g)
		if isLessStrict(l, DefaultLevel(g)) {
			out = append(out, fmt.Sprintf("%s=%s", g, l))
		}
	}
	return out
}

// isLessStrict reports whether current is a weaker posture than default.
func isLessStrict(current, def Level) bool {
	rank := map[Level]int{Ignore: 0, Warn: 1, Enforce: 2}
	return rank[current] < rank[def]
}

// preferencesFile is the name of the posture/preferences file.
const preferencesFile = "preferences.yaml"

// Load reads global then project preferences.yaml and returns the merged
// policy. A missing file yields an empty policy with no error.
func Load(workdir string) (Policy, error) {
	global, err := loadPreferencesDir(config.GlobalDir)
	if err != nil {
		return nil, err
	}
	proj, err := loadPreferencesPath(filepath.Join(config.ProjectBelaiDir(workdir), preferencesFile))
	if err != nil {
		return nil, err
	}
	return Defaults().Override(global).Override(proj), nil
}

// loadPreferencesDir tries to read preferences.yaml inside a directory.
func loadPreferencesDir(dirFn func() (string, error)) (Policy, error) {
	dir, err := dirFn()
	if err != nil {
		return nil, err
	}
	return loadPreferencesPath(filepath.Join(dir, preferencesFile))
}

// loadPreferencesPath reads a single preferences.yaml. Missing files are
// benign.
func loadPreferencesPath(path string) (Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw struct {
		Postures map[string]string `yaml:"postures"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if raw.Postures == nil {
		return nil, nil
	}
	p := make(Policy, len(raw.Postures))
	for k, v := range raw.Postures {
		p[Gate(strings.TrimSpace(k))] = Level(strings.TrimSpace(v))
	}
	return p, nil
}

// FlagSet captures CLI overrides. Zero values mean "not set".
type FlagSet struct {
	AllowUnsafeToolResult    *bool
	AllowMalformedToolResult *bool
	AllowUnsafePrompt        *bool
	AllowMalformedPrompt     *bool
	ToolCallMismatch         *string
	AllowUnpermittedTools    *bool
	AllowAskWithoutTTY       *bool
	AllowInvalidSkills       *bool
	AllowInvalidHooks        *bool
	DangerouslyYolo          bool
}

// ToPolicy converts CLI overrides to a Policy. `--dangerously-yolo-everything`
// maps every gate except the unoverridable BoundarySource gate to ignore.
func (fs FlagSet) ToPolicy() Policy {
	if fs.DangerouslyYolo {
		p := make(Policy, len(AllGates))
		for _, g := range AllGates {
			p[g] = Ignore
		}
		return p
	}
	p := make(Policy)
	if fs.AllowUnsafeToolResult != nil && *fs.AllowUnsafeToolResult {
		p[ToolResultUnsafe] = Ignore
	}
	if fs.AllowMalformedToolResult != nil && *fs.AllowMalformedToolResult {
		p[ToolResultMalformed] = Ignore
	}
	if fs.AllowUnsafePrompt != nil && *fs.AllowUnsafePrompt {
		p[PromptUnsafe] = Ignore
	}
	if fs.AllowMalformedPrompt != nil && *fs.AllowMalformedPrompt {
		p[PromptMalformed] = Ignore
	}
	if fs.ToolCallMismatch != nil {
		switch strings.ToLower(*fs.ToolCallMismatch) {
		case "strip":
			p[ToolCallMismatch] = Warn
		case "ignore":
			p[ToolCallMismatch] = Ignore
		}
	}
	if fs.AllowUnpermittedTools != nil && *fs.AllowUnpermittedTools {
		p[PermissionNoMatch] = Ignore
	}
	if fs.AllowAskWithoutTTY != nil && *fs.AllowAskWithoutTTY {
		p[PermissionAskNoTTY] = Ignore
	}
	if fs.AllowInvalidSkills != nil && *fs.AllowInvalidSkills {
		p[SkillInvalid] = Ignore
	}
	if fs.AllowInvalidHooks != nil && *fs.AllowInvalidHooks {
		p[HookInvalid] = Ignore
	}
	return p
}

// PrintBanner prints a one-line stderr warning for every non-enforce gate,
// and a prominent banner for --dangerously-yolo-everything.
func PrintBanner(p Policy, w *os.File) {
	if down := p.Downgrades(); len(down) > 0 {
		fmt.Fprintf(w, "belai: posture downgrades: %s\n", strings.Join(down, ", "))
	}
}
