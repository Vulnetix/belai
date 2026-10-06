package config

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/shellsafe"
)

// MaxBashRewriteRules bounds the rewrite table.
const MaxBashRewriteRules = 64

// BashRewriteSettings is the user's Bash rewrite table (docs/bash-rewrite.md):
// rules that change the command word of a model's Bash line before permission
// matching, such as `npm` to `pnpm`. The default is no rules. It is read from
// the user's own settings layers only; a repository-visible project layer may
// turn the table off and can never supply a rule or turn it on.
type BashRewriteSettings struct {
	// Enabled switches the table off (false) without deleting it. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// Rules are tried in order and the first match wins for each command.
	Rules []BashRewriteRule `json:"rules,omitempty"`
}

// BashRewriteRule rewrites a command whose argv starts with Match. Both sides
// are space-separated plain words.
type BashRewriteRule struct {
	// Match is the leading words of a command, such as "npm" or "npm install".
	Match string `json:"match"`
	// Replace is what those words become, such as "pnpm" or "pnpm add".
	Replace string `json:"replace"`
}

// BashRewriteRules returns the active rules, in order: none when the table is
// absent or switched off.
func (s Settings) BashRewriteRules() []shellsafe.RewriteRule {
	b := s.BashRewrite
	if b == nil || (b.Enabled != nil && !*b.Enabled) {
		return nil
	}
	out := make([]shellsafe.RewriteRule, 0, len(b.Rules))
	for _, r := range b.Rules {
		rule := shellsafe.RewriteRule{Match: strings.Fields(r.Match), Replace: strings.Fields(r.Replace)}
		if shellsafe.ValidRewriteRule(rule) != nil {
			// Validation rejects these at load; a hand-built Settings that
			// skipped it gets no rewrite rather than a doubtful one.
			return nil
		}
		out = append(out, rule)
	}
	return out
}

// ValidateBashRewrite rejects a table with too many rules or a rule that is not
// plain words, naming the key.
func ValidateBashRewrite(s Settings) error {
	b := s.BashRewrite
	if b == nil {
		return nil
	}
	if len(b.Rules) > MaxBashRewriteRules {
		return fmt.Errorf("bash_rewrite.rules has %d rules; at most %d", len(b.Rules), MaxBashRewriteRules)
	}
	for i, r := range b.Rules {
		rule := shellsafe.RewriteRule{Match: strings.Fields(r.Match), Replace: strings.Fields(r.Replace)}
		if err := shellsafe.ValidRewriteRule(rule); err != nil {
			return fmt.Errorf("bash_rewrite.rules[%d]: %w", i, err)
		}
	}
	return nil
}

// mergeBashRewrite folds in over cur. A user layer replaces the rules as a
// whole when it names any, and sets the switch. From the project layer only an
// explicit enabled:false takes effect; its rules are dropped.
func mergeBashRewrite(cur, in *BashRewriteSettings, project bool) *BashRewriteSettings {
	if in == nil {
		return cur
	}
	out := &BashRewriteSettings{}
	if cur != nil {
		*out = *cur
	}
	if project {
		if in.Enabled == nil || *in.Enabled {
			return cur
		}
		f := false
		out.Enabled = &f
		return out
	}
	if in.Enabled != nil {
		b := *in.Enabled
		out.Enabled = &b
	}
	if in.Rules != nil {
		out.Rules = append([]BashRewriteRule(nil), in.Rules...)
	}
	return out
}
