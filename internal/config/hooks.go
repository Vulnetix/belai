package config

import (
	"fmt"
	"regexp"
	"sort"
)

// MaxAllowedPrograms bounds hooks.allowed_programs.
const MaxAllowedPrograms = 32

// allowedProgramRE is a bare program name: no path, no space, nothing a shell
// reads specially. internal/hooks holds the same rule for the names it accepts.
var allowedProgramRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ValidProgramName reports whether s can be listed in hooks.allowed_programs.
func ValidProgramName(s string) bool { return allowedProgramRE.MatchString(s) }

// ValidateHooks checks hooks.allowed_programs: the bare names of programs a hook
// bundle's commands may run from PATH. It is a user-layer key, so a repository
// can never name one.
func ValidateHooks(s Settings) error {
	if s.Hooks == nil {
		return nil
	}
	if n := len(s.Hooks.AllowedPrograms); n > MaxAllowedPrograms {
		return fmt.Errorf("hooks.allowed_programs lists %d programs; the most is %d", n, MaxAllowedPrograms)
	}
	for _, p := range s.Hooks.AllowedPrograms {
		if !ValidProgramName(p) {
			return fmt.Errorf("hooks.allowed_programs entry %q must be a bare program name (letters, digits, . _ + -, at most 64)", p)
		}
	}
	return nil
}

// AllowedProgramList is hooks.allowed_programs, sorted and without duplicates.
func (s *HooksSettings) AllowedProgramList() []string {
	if s == nil || len(s.AllowedPrograms) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range s.AllowedPrograms {
		if !seen[p] && ValidProgramName(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
