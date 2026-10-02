package agent

import (
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/tools"
)

// rewriteBashArgs applies the user's bash_rewrite table (docs/bash-rewrite.md)
// to a Bash call's arguments. It runs once, before any permission decision, so
// the permission rules, the ask, the hooks and the executor all see the line
// that will run.
//
// A rewrite never launders a denied command: when an explicit deny or block
// rule already matches the line the model sent, the call is left as sent and
// the ordinary permission path refuses it. The rewritten line is then judged
// from scratch by decidePermission like any other line, so a deny rule for what
// it became still fires and an allow rule must cover all of it. No model is
// asked.
func (s *Session) rewriteBashArgs(args map[string]any) (map[string]any, string) {
	rules := s.settings.BashRewriteRules()
	if len(rules) == 0 {
		return args, ""
	}
	cmd, _ := args["command"].(string)
	if dec, rule := s.perms.Explain("Bash", cmd); dec == permissions.DecisionBlock && rule != "" {
		return args, ""
	}
	next, note, changed := tools.RewriteBash(rules, args)
	if !changed {
		return args, ""
	}
	return next, note
}
