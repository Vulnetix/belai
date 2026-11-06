package config

import "fmt"

// LintLoopSettings configures the per-edit lint loop (docs/lint.md): after a
// successful Write, Edit or apply_patch the harness runs the repository's
// detected linter on the edited file and returns a bounded, classified list of
// findings on the same tool result. The argv comes from the fixed table in
// internal/lintdetect, never from a model or a profile.
//
// The loop runs a program on the user's account after every edit, so it is the
// user's opt-in: it is off unless a user layer, BELAI_LINT_LOOP or --lint-loop
// turns it on. The project layer, which a cloned repository controls, can only
// turn it off and lower a number.
type LintLoopSettings struct {
	// Enabled switches the loop. nil means off, and only an explicit true
	// enables it.
	Enabled *bool `json:"enabled,omitempty"`
	// TimeoutMS bounds one linter run. Default 10000, 1000 to 60000.
	TimeoutMS *int `json:"timeout_ms,omitempty"`
	// MaxAttempts is how many passes in a row may report the same findings
	// for a file before the loop stops showing them and tells the model to
	// move on. Default 3, 1 to 6.
	MaxAttempts *int `json:"max_attempts,omitempty"`
	// MaxRows is the most finding rows one block shows. Default 10, 1 to 20.
	MaxRows *int `json:"max_rows,omitempty"`
}

// Lint loop defaults and bounds.
const (
	DefaultLintLoopTimeoutMS   = 10_000
	DefaultLintLoopMaxAttempts = 3
	DefaultLintLoopMaxRows     = 10

	minLintLoopTimeoutMS = 1_000
	maxLintLoopTimeoutMS = 60_000
	maxLintLoopAttempts  = 6
	maxLintLoopRows      = 20
)

// lintLoopBound is the closed range of one numeric key.
type lintLoopBound struct {
	key      string
	lo, hi   int
	get      func(*LintLoopSettings) **int
	accessor func(Settings) int
}

var lintLoopBounds = []lintLoopBound{
	{"timeout_ms", minLintLoopTimeoutMS, maxLintLoopTimeoutMS,
		func(l *LintLoopSettings) **int { return &l.TimeoutMS }, Settings.LintLoopTimeoutMS},
	{"max_attempts", 1, maxLintLoopAttempts,
		func(l *LintLoopSettings) **int { return &l.MaxAttempts }, Settings.LintLoopMaxAttempts},
	{"max_rows", 1, maxLintLoopRows,
		func(l *LintLoopSettings) **int { return &l.MaxRows }, Settings.LintLoopMaxRows},
}

// LintLoopEnabled reports whether the per-edit lint loop runs. Default off.
func (s Settings) LintLoopEnabled() bool {
	return s.LintLoop != nil && s.LintLoop.Enabled != nil && *s.LintLoop.Enabled
}

func (s Settings) lintLoopInt(pick func(*LintLoopSettings) *int, lo, hi, def int) int {
	if s.LintLoop == nil {
		return def
	}
	if p := pick(s.LintLoop); p != nil && *p >= lo && *p <= hi {
		return *p
	}
	return def
}

// LintLoopTimeoutMS is the bound on one linter run. An out-of-range value
// falls back to the default, so a bad value never reaches the runner.
func (s Settings) LintLoopTimeoutMS() int {
	return s.lintLoopInt(func(l *LintLoopSettings) *int { return l.TimeoutMS }, minLintLoopTimeoutMS, maxLintLoopTimeoutMS, DefaultLintLoopTimeoutMS)
}

// LintLoopMaxAttempts is how many same-findings passes a file gets.
func (s Settings) LintLoopMaxAttempts() int {
	return s.lintLoopInt(func(l *LintLoopSettings) *int { return l.MaxAttempts }, 1, maxLintLoopAttempts, DefaultLintLoopMaxAttempts)
}

// LintLoopMaxRows is the most rows one block shows.
func (s Settings) LintLoopMaxRows() int {
	return s.lintLoopInt(func(l *LintLoopSettings) *int { return l.MaxRows }, 1, maxLintLoopRows, DefaultLintLoopMaxRows)
}

// ValidateLintLoop rejects a lint_loop key outside its range, naming the key. A
// repository's bad value never gets here: mergeLintLoop drops it.
func ValidateLintLoop(s Settings) error {
	l := s.LintLoop
	if l == nil {
		return nil
	}
	for _, b := range lintLoopBounds {
		if p := *b.get(l); p != nil && (*p < b.lo || *p > b.hi) {
			return fmt.Errorf("lint_loop.%s %d must be between %d and %d", b.key, *p, b.lo, b.hi)
		}
	}
	return nil
}

// mergeLintLoop folds in over cur. A user layer or a flag sets any key in
// range. The project layer only narrows: it can set enabled to false, never to
// true, and it lowers a number only when the value is in range and below the
// one in force. An out-of-range value from any layer is dropped.
func mergeLintLoop(cur, in *LintLoopSettings, project bool) *LintLoopSettings {
	if in == nil {
		return cur
	}
	out := &LintLoopSettings{}
	if cur != nil {
		*out = *cur
	}
	if in.Enabled != nil && !(project && *in.Enabled) {
		v := *in.Enabled
		out.Enabled = &v
	}
	for _, b := range lintLoopBounds {
		p := *b.get(in)
		if p == nil || *p < b.lo || *p > b.hi {
			continue
		}
		if project && *p >= b.accessor(Settings{LintLoop: out}) {
			continue
		}
		v := *p
		*b.get(out) = &v
	}
	if cur == nil && out.Enabled == nil && out.TimeoutMS == nil && out.MaxAttempts == nil && out.MaxRows == nil {
		return nil
	}
	return out
}

// lintLoopIgnored names the project keys mergeLintLoop would not apply on top
// of user, for the note Resolve prints.
func lintLoopIgnored(user, proj *LintLoopSettings) []string {
	if proj == nil {
		return nil
	}
	merged := mergeLintLoop(user, proj, true)
	var out []string
	if proj.Enabled != nil && *proj.Enabled {
		out = append(out, "lint_loop.enabled")
	}
	for _, b := range lintLoopBounds {
		p := *b.get(proj)
		if p == nil {
			continue
		}
		var got *int
		if merged != nil {
			got = *b.get(merged)
		}
		if got == nil || *got != *p {
			out = append(out, "lint_loop."+b.key)
		}
	}
	return out
}

// lintLoopEnv reads BELAI_LINT_LOOP. It is the user's own environment, so a
// true value turns the loop on and a false one turns it off; anything else is
// no layer at all.
func lintLoopEnv(v string) *LintLoopSettings {
	if b := envBool(v); b != nil {
		return &LintLoopSettings{Enabled: b}
	}
	return nil
}
