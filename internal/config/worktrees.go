package config

import (
	"fmt"
	"strings"
)

// WorktreeSettings governs worktree sessions: the argv a worktree runs when it
// is made and when it is removed, the command that brings its branch up to
// date with its base, and whether a finished worktree stays on disk
// (docs/worktrees.md).
//
// Setup and teardown are code the harness executes, so every key is the user's
// own: a project layer can set none of them, and may only turn update_mode off.
type WorktreeSettings struct {
	// Setup is a list of argv arrays run in order inside a new worktree, once
	// it exists and before any work starts. Each runs without a shell. A failure
	// removes the worktree.
	Setup [][]string `json:"setup,omitempty"`
	// Teardown is a list of argv arrays run in order before a worktree is
	// removed. A failure keeps the worktree on disk.
	Teardown [][]string `json:"teardown,omitempty"`
	// TimeoutSeconds bounds one argv. Default 120, clamped to 1 through 600.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
	// UpdateMode is how a worktree's branch is brought up to date with its
	// base: off (default), merge or rebase.
	UpdateMode *string `json:"update_mode,omitempty"`
	// KeepOnExit keeps the directory of a worktree session opened with
	// /worktree new after its agent finishes. Default true.
	KeepOnExit *bool `json:"keep_on_exit,omitempty"`
	// Network is whether setup and teardown may use the network: deny
	// (default) or allow.
	Network *string `json:"network,omitempty"`
}

// Worktree settings values and bounds.
const (
	WorktreeUpdateOff    = "off"
	WorktreeUpdateMerge  = "merge"
	WorktreeUpdateRebase = "rebase"

	WorktreeNetworkDeny  = "deny"
	WorktreeNetworkAllow = "allow"

	DefaultWorktreeTimeoutSeconds = 120
	MaxWorktreeTimeoutSeconds     = 600

	// MaxWorktreeCommands, MaxWorktreeArgs and MaxWorktreeArgBytes bound a
	// list of argv arrays.
	MaxWorktreeCommands = 8
	MaxWorktreeArgs     = 32
	MaxWorktreeArgBytes = 512

	// WorktreeLabel marks a board card filed by /worktree new; a worker that
	// claims it works in a worktree whatever its profile's isolation says.
	WorktreeLabel = "worktree"
)

// WorktreeUpdateModes is the closed set of worktrees.update_mode values.
var WorktreeUpdateModes = []string{WorktreeUpdateOff, WorktreeUpdateMerge, WorktreeUpdateRebase}

// WorktreeNetworkModes is the closed set of worktrees.network values.
var WorktreeNetworkModes = []string{WorktreeNetworkDeny, WorktreeNetworkAllow}

// WorktreeSetupArgv is worktrees.setup: copies, so a caller cannot edit the
// settings.
func (s Settings) WorktreeSetupArgv() [][]string {
	if s.Worktrees == nil {
		return nil
	}
	return cloneArgvs(s.Worktrees.Setup)
}

// WorktreeTeardownArgv is worktrees.teardown.
func (s Settings) WorktreeTeardownArgv() [][]string {
	if s.Worktrees == nil {
		return nil
	}
	return cloneArgvs(s.Worktrees.Teardown)
}

func cloneArgvs(in [][]string) [][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([][]string, len(in))
	for i, a := range in {
		out[i] = append([]string(nil), a...)
	}
	return out
}

// WorktreeTimeout is the bound on one setup or teardown argv, in seconds.
func (s Settings) WorktreeTimeout() int {
	if s.Worktrees == nil || s.Worktrees.TimeoutSeconds == nil {
		return DefaultWorktreeTimeoutSeconds
	}
	return min(max(*s.Worktrees.TimeoutSeconds, 1), MaxWorktreeTimeoutSeconds)
}

// WorktreeUpdate is the update mode: off, merge or rebase. Default off, and a
// value this build does not know is off, so a typo never starts an update.
func (s Settings) WorktreeUpdate() string {
	if s.Worktrees == nil || s.Worktrees.UpdateMode == nil {
		return WorktreeUpdateOff
	}
	switch v := strings.ToLower(strings.TrimSpace(*s.Worktrees.UpdateMode)); v {
	case WorktreeUpdateMerge, WorktreeUpdateRebase:
		return v
	}
	return WorktreeUpdateOff
}

// WorktreeKeepOnExit reports whether a /worktree new session's directory stays
// after its agent finishes. Default true.
func (s Settings) WorktreeKeepOnExit() bool {
	return s.Worktrees == nil || s.Worktrees.KeepOnExit == nil || *s.Worktrees.KeepOnExit
}

// WorktreeNetworkAllowed reports whether setup and teardown may use the
// network. Default false, and any value but allow is deny.
func (s Settings) WorktreeNetworkAllowed() bool {
	if s.Worktrees == nil || s.Worktrees.Network == nil {
		return false
	}
	return strings.ToLower(strings.TrimSpace(*s.Worktrees.Network)) == WorktreeNetworkAllow
}

// ValidWorktreeArgv checks one argv: argv[0] is a bare program name (looked up
// on PATH), or a relative path that stays under the worktree, or an absolute
// path; every word is non-empty text without a control character and within
// the length cap. There is no shell, so a word is never interpreted, but the
// limits keep a settings file from smuggling a payload.
func ValidWorktreeArgv(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("an argv needs at least a program")
	}
	if len(argv) > MaxWorktreeArgs {
		return fmt.Errorf("an argv has %d words; the most is %d", len(argv), MaxWorktreeArgs)
	}
	for i, w := range argv {
		if len(w) > MaxWorktreeArgBytes {
			return fmt.Errorf("word %d is longer than %d bytes", i, MaxWorktreeArgBytes)
		}
		for _, r := range w {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("word %d holds a control character", i)
			}
		}
	}
	prog := argv[0]
	if prog == "" || strings.TrimSpace(prog) != prog {
		return fmt.Errorf("the program must not be empty or padded")
	}
	if ValidProgramName(prog) {
		return nil
	}
	if strings.HasPrefix(prog, "-") || strings.ContainsAny(prog, " \t$`;&|<>*?(){}[]!#~\\\"'") {
		return fmt.Errorf("program %q is not a program name or a plain path", prog)
	}
	for _, part := range strings.Split(prog, "/") {
		if part == ".." {
			return fmt.Errorf("program %q must not climb out of its directory", prog)
		}
	}
	if !strings.Contains(prog, "/") {
		return fmt.Errorf("program %q is not a valid program name", prog)
	}
	return nil
}

func validateArgvList(key string, list [][]string) error {
	if len(list) > MaxWorktreeCommands {
		return fmt.Errorf("%s lists %d commands; the most is %d", key, len(list), MaxWorktreeCommands)
	}
	for i, argv := range list {
		if err := ValidWorktreeArgv(argv); err != nil {
			return fmt.Errorf("%s[%d]: %w", key, i, err)
		}
	}
	return nil
}

// ValidateWorktrees checks the worktrees block.
func ValidateWorktrees(s Settings) error {
	w := s.Worktrees
	if w == nil {
		return nil
	}
	if err := validateArgvList("worktrees.setup", w.Setup); err != nil {
		return err
	}
	if err := validateArgvList("worktrees.teardown", w.Teardown); err != nil {
		return err
	}
	if w.TimeoutSeconds != nil && (*w.TimeoutSeconds < 1 || *w.TimeoutSeconds > MaxWorktreeTimeoutSeconds) {
		return fmt.Errorf("worktrees.timeout_seconds %d must be between 1 and %d", *w.TimeoutSeconds, MaxWorktreeTimeoutSeconds)
	}
	if w.UpdateMode != nil && !inFold(WorktreeUpdateModes, *w.UpdateMode) {
		return fmt.Errorf("worktrees.update_mode must be one of off, merge or rebase, got %q", *w.UpdateMode)
	}
	if w.Network != nil && !inFold(WorktreeNetworkModes, *w.Network) {
		return fmt.Errorf("worktrees.network must be deny or allow, got %q", *w.Network)
	}
	return nil
}

func inFold(set []string, v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// mergeWorktrees folds from over base. A user layer sets any key. The project
// layer, which a cloned repository controls, can name no argv, timeout, network
// or keep rule: the only thing it may do is set update_mode to off.
func mergeWorktrees(base, from *WorktreeSettings, project bool) *WorktreeSettings {
	if from == nil {
		return base
	}
	out := &WorktreeSettings{}
	if base != nil {
		*out = *base
	}
	if project {
		if from.UpdateMode == nil || strings.ToLower(strings.TrimSpace(*from.UpdateMode)) != WorktreeUpdateOff {
			return base
		}
		v := WorktreeUpdateOff
		out.UpdateMode = &v
		return out
	}
	if from.Setup != nil {
		out.Setup = cloneArgvs(from.Setup)
	}
	if from.Teardown != nil {
		out.Teardown = cloneArgvs(from.Teardown)
	}
	if from.TimeoutSeconds != nil {
		v := *from.TimeoutSeconds
		out.TimeoutSeconds = &v
	}
	if from.UpdateMode != nil {
		v := strings.ToLower(strings.TrimSpace(*from.UpdateMode))
		out.UpdateMode = &v
	}
	if from.KeepOnExit != nil {
		v := *from.KeepOnExit
		out.KeepOnExit = &v
	}
	if from.Network != nil {
		v := strings.ToLower(strings.TrimSpace(*from.Network))
		out.Network = &v
	}
	return out
}

// worktreesIgnored names the project keys mergeWorktrees would not apply, for
// the note Resolve prints.
func worktreesIgnored(user, proj *WorktreeSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	if len(proj.Setup) > 0 {
		out = append(out, "worktrees.setup")
	}
	if len(proj.Teardown) > 0 {
		out = append(out, "worktrees.teardown")
	}
	if proj.TimeoutSeconds != nil {
		out = append(out, "worktrees.timeout_seconds")
	}
	if proj.UpdateMode != nil && strings.ToLower(strings.TrimSpace(*proj.UpdateMode)) != WorktreeUpdateOff {
		out = append(out, "worktrees.update_mode")
	}
	if proj.KeepOnExit != nil {
		out = append(out, "worktrees.keep_on_exit")
	}
	if proj.Network != nil {
		out = append(out, "worktrees.network")
	}
	return out
}
