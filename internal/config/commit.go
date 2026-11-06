package config

import (
	"fmt"
	"path"
	"strings"
	"unicode"
)

// Commit modes (docs/commit-workflow.md). "off" never commits on its own,
// "ask" shows the paths and the message and commits only on a yes, "auto"
// commits what the harness decided belongs to the task.
const (
	CommitModeOff  = "off"
	CommitModeAsk  = "ask"
	CommitModeAuto = "auto"
)

// CommitModeNames is the closed set of commit_mode values.
var CommitModeNames = []string{CommitModeOff, CommitModeAsk, CommitModeAuto}

// Bounds on commit_exclude, so a settings file cannot build an unbounded
// matcher.
const (
	MaxCommitExclude       = 64
	MaxCommitExcludeLength = 200
)

// CommitModeEffective is how a completed goal's changed files are committed. The
// legacy auto_commit_per_task=true means "auto" when commit_mode is unset; an
// explicit commit_mode always wins, and any unrecognised value is "off"
// (ValidateCommitWorkflow refuses it at load, this is the fail-closed floor).
func (s Settings) CommitModeEffective() string {
	switch strings.ToLower(strings.TrimSpace(s.CommitMode)) {
	case CommitModeAsk:
		return CommitModeAsk
	case CommitModeAuto:
		return CommitModeAuto
	case CommitModeOff:
		return CommitModeOff
	case "":
		if s.AutoCommitPerTaskEnabled() {
			return CommitModeAuto
		}
	}
	return CommitModeOff
}

// CommitDraftEnabled reports whether a model drafts the commit message's words
// (commit_draft). Default off: the deterministic message is used.
func (s Settings) CommitDraftEnabled() bool {
	return s.CommitDraft != nil && *s.CommitDraft
}

// CommitExcludeGlobs returns the extra path patterns the harness never commits.
func (s Settings) CommitExcludeGlobs() []string {
	return append([]string(nil), s.CommitExclude...)
}

// ValidCommitExclude reports whether one commit_exclude pattern is acceptable:
// a relative slash path glob that path.Match accepts, with no "..", no control
// character and a bounded length. A trailing slash names a directory.
func ValidCommitExclude(p string) bool {
	if p == "" || len(p) > MaxCommitExcludeLength || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if seg == ".." || seg == "" {
			return false
		}
	}
	_, err := path.Match(strings.TrimSuffix(p, "/"), "x")
	return err == nil
}

// ValidateCommitWorkflow checks commit_mode and commit_exclude.
func ValidateCommitWorkflow(s Settings) error {
	if v := strings.ToLower(strings.TrimSpace(s.CommitMode)); v != "" {
		ok := false
		for _, n := range CommitModeNames {
			ok = ok || v == n
		}
		if !ok {
			return fmt.Errorf("commit_mode must be one of off, ask or auto, got %q", s.CommitMode)
		}
	}
	if len(s.CommitExclude) > MaxCommitExclude {
		return fmt.Errorf("commit_exclude holds %d patterns, at most %d", len(s.CommitExclude), MaxCommitExclude)
	}
	for _, p := range s.CommitExclude {
		if !ValidCommitExclude(p) {
			return fmt.Errorf("commit_exclude pattern %q is not a relative glob of at most %d characters", p, MaxCommitExcludeLength)
		}
	}
	return nil
}

// projectCommitExclude returns the project layer's patterns that may be added
// on top of the user's: valid ones, in order, within what room is left. The
// second result lists what was left out, for a note.
func projectCommitExclude(user, proj []string) (keep []string, dropped int) {
	room := MaxCommitExclude - len(user)
	have := map[string]bool{}
	for _, p := range user {
		have[p] = true
	}
	for _, p := range proj {
		switch {
		case !ValidCommitExclude(p), len(keep) >= room:
			dropped++
		case have[p]:
		default:
			have[p] = true
			keep = append(keep, p)
		}
	}
	return keep, dropped
}
