package main

import (
	"github.com/vulnetix/belai/internal/config"
)

// applyLintLoopFlag turns the per-edit lint loop on (docs/lint.md) when
// -lint-loop is set. The flag is the user's own choice, as a settings entry in
// their own layers is, so it may enable what the project layer cannot. It never
// turns the loop off: BELAI_LINT_LOOP=0 and a settings entry do that, and a
// repository's setting cannot reach this function.
func applyLintLoopFlag(s *config.Settings, on bool) {
	if !on {
		return
	}
	l := config.LintLoopSettings{}
	if s.LintLoop != nil {
		l = *s.LintLoop
	}
	yes := true
	l.Enabled = &yes
	s.LintLoop = &l
}
