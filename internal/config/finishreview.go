package config

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// FinishReviewSettings configures the finish review gate (docs/finish-review.md):
// the harness-computed check of the working tree that runs before a goal is
// accepted as complete. No model is asked whether anything is safe or real.
//
// The review only adds scrutiny, so the project layer, which a cloned
// repository controls, can only tighten it: it cannot switch the gate off or
// lower the rounds, and it can add scratch patterns but never remove one of the
// built-in table. The user's layers, the BELAI_FINISH_REVIEW environment
// variable and -no-finish-review are the only ways to switch it off.
type FinishReviewSettings struct {
	// Enabled switches the gate. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxRounds is how many times the gate may send a completion back to the
	// model for the findings it lists. Default 2, 0 to 5; 0 never sends one
	// back (findings are still named in the final report).
	MaxRounds *int `json:"max_rounds,omitempty"`
	// ScratchPatterns are extra file-name patterns that mark an untracked file
	// as scratch, added to the built-in table. A pattern with no slash matches
	// a file's base name, one with a slash matches the whole relative path.
	ScratchPatterns []string `json:"scratch_patterns,omitempty"`
}

// Finish review defaults and bounds.
const (
	DefaultFinishReviewMaxRounds = 2
	maxFinishReviewRounds        = 5
	// MaxFinishReviewPatterns bounds the extra scratch patterns in force.
	MaxFinishReviewPatterns = 32
	maxFinishReviewPatLen   = 80
)

// ValidScratchPattern reports whether p can be a scratch pattern: 1 to 80 of
// letters, digits and . _ - / @ + * ?, at least one of them not a wildcard, no
// leading slash or ".." component, and a well-formed path.Match pattern.
func ValidScratchPattern(p string) bool {
	if p == "" || len(p) > maxFinishReviewPatLen || strings.HasPrefix(p, "/") {
		return false
	}
	literal := false
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/', r == '@', r == '+':
			literal = true
		case r == '*', r == '?':
		default:
			return false
		}
	}
	if !literal {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	_, err := path.Match(p, "x")
	return err == nil
}

// FinishReviewEnabled reports whether the finish review gate runs. Default on.
func (s Settings) FinishReviewEnabled() bool {
	return s.FinishReview == nil || s.FinishReview.Enabled == nil || *s.FinishReview.Enabled
}

// FinishReviewMaxRounds is how many times the gate may send a completion back.
// An out-of-range value falls back to the default, so a bad value can never
// reach the loop.
func (s Settings) FinishReviewMaxRounds() int {
	if s.FinishReview == nil || s.FinishReview.MaxRounds == nil {
		return DefaultFinishReviewMaxRounds
	}
	if v := *s.FinishReview.MaxRounds; v >= 0 && v <= maxFinishReviewRounds {
		return v
	}
	return DefaultFinishReviewMaxRounds
}

// FinishReviewScratchPatterns are the extra scratch patterns in force: the
// valid ones, deduplicated, at most [MaxFinishReviewPatterns].
func (s Settings) FinishReviewScratchPatterns() []string {
	if s.FinishReview == nil {
		return nil
	}
	return addScratchPatterns(nil, s.FinishReview.ScratchPatterns)
}

// addScratchPatterns appends the valid, new patterns of add to base, up to the
// cap, and returns a new slice.
func addScratchPatterns(base, add []string) []string {
	out := append([]string(nil), base...)
	for _, p := range add {
		if len(out) >= MaxFinishReviewPatterns {
			break
		}
		if !ValidScratchPattern(p) {
			continue
		}
		dup := false
		for _, q := range out {
			if q == p {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, p)
		}
	}
	return out
}

// ValidateFinishReview rejects a malformed finish_review block, naming the key:
// max_rounds outside 0 to 5, a pattern that fails [ValidScratchPattern], or more
// than [MaxFinishReviewPatterns] patterns. A repository's bad value never gets
// here: mergeFinishReview drops it.
func ValidateFinishReview(s Settings) error {
	f := s.FinishReview
	if f == nil {
		return nil
	}
	if f.MaxRounds != nil && (*f.MaxRounds < 0 || *f.MaxRounds > maxFinishReviewRounds) {
		return fmt.Errorf("finish_review.max_rounds %d must be between 0 and %d", *f.MaxRounds, maxFinishReviewRounds)
	}
	if len(f.ScratchPatterns) > MaxFinishReviewPatterns {
		return fmt.Errorf("finish_review.scratch_patterns holds %d patterns; at most %d", len(f.ScratchPatterns), MaxFinishReviewPatterns)
	}
	for _, p := range f.ScratchPatterns {
		if !ValidScratchPattern(p) {
			return fmt.Errorf("finish_review.scratch_patterns: %q is not a valid pattern (1 to %d of letters, digits and . _ - / @ + * ?, one not a wildcard)", p, maxFinishReviewPatLen)
		}
	}
	return nil
}

// mergeFinishReview folds in over cur. A user layer or a flag sets any key in
// range and adds its patterns. The project layer only tightens: it cannot turn
// the gate off, applies max_rounds only when it is in range and above the value
// in force, and adds valid patterns up to the cap.
func mergeFinishReview(cur, in *FinishReviewSettings, project bool) *FinishReviewSettings {
	if in == nil {
		return cur
	}
	out := &FinishReviewSettings{}
	if cur != nil {
		*out = *cur
		out.ScratchPatterns = append([]string(nil), cur.ScratchPatterns...)
	}
	if in.Enabled != nil && !project {
		v := *in.Enabled
		out.Enabled = &v
	}
	if in.MaxRounds != nil {
		v := *in.MaxRounds
		inRange := v >= 0 && v <= maxFinishReviewRounds
		if !project || (inRange && v > (Settings{FinishReview: out}).FinishReviewMaxRounds()) {
			out.MaxRounds = &v
		}
	}
	out.ScratchPatterns = addScratchPatterns(out.ScratchPatterns, in.ScratchPatterns)
	if len(out.ScratchPatterns) == 0 {
		out.ScratchPatterns = nil
	}
	if cur == nil && out.Enabled == nil && out.MaxRounds == nil && out.ScratchPatterns == nil {
		return nil
	}
	return out
}

// finishReviewIgnored names the project keys mergeFinishReview would not apply
// on top of user, for the note Resolve prints.
func finishReviewIgnored(user, proj *FinishReviewSettings) []string {
	if proj == nil {
		return nil
	}
	merged := mergeFinishReview(user, proj, true)
	var out []string
	if proj.Enabled != nil && !*proj.Enabled {
		out = append(out, "finish_review.enabled")
	}
	if proj.MaxRounds != nil && (merged.MaxRounds == nil || *merged.MaxRounds != *proj.MaxRounds) {
		out = append(out, "finish_review.max_rounds")
	}
	have := map[string]bool{}
	if merged != nil {
		for _, p := range merged.ScratchPatterns {
			have[p] = true
		}
	}
	for _, p := range proj.ScratchPatterns {
		if !have[p] {
			out = append(out, "finish_review.scratch_patterns")
			break
		}
	}
	return out
}

// finishReviewEnv reads BELAI_FINISH_REVIEW. Only a false value (0, false)
// counts, and it switches the gate off: the variable can never turn on a gate
// a settings layer turned off. Anything else is no layer at all.
func finishReviewEnv(v string) *FinishReviewSettings {
	if b := envBool(v); b != nil && !*b {
		return &FinishReviewSettings{Enabled: b}
	}
	return nil
}

// FinishReviewDir returns <GlobalDir>/finish_review, where the baseline of the
// files a goal started with is kept so a resumed goal does not mistake them for
// its own scratch files (docs/finish-review.md). It holds file names only. Like
// the rest of the state directory the OS sandbox hides it from every command,
// so the model cannot edit its own baseline.
func FinishReviewDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "finish_review"), nil
}

// FinishReviewFile is the baseline file of one goal: its id must be plain
// identifier characters, and the result is checked to stay inside
// [FinishReviewDir].
func FinishReviewFile(goalID string) (string, error) {
	dir, err := FinishReviewDir()
	if err != nil {
		return "", err
	}
	if !plainStateName(goalID) {
		return "", fmt.Errorf("%q is not a goal id", goalID)
	}
	p := filepath.Join(dir, goalID+".json")
	if !within(dir, p) {
		return "", fmt.Errorf("%q is not a goal id", goalID)
	}
	return p, nil
}
