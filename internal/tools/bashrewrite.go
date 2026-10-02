package tools

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/shellsafe"
)

// RewriteBash applies the user's bash_rewrite rules (docs/bash-rewrite.md) to a
// Bash call's arguments and returns the arguments that will run, a
// harness-composed note for the model, and whether anything changed. The args
// map is never modified: a changed call gets a copy.
//
// This is the only place a model's command is replaced, and it runs before
// permission matching, so the line that is permission-checked, asked about and
// executed is the rewritten one. The caller must additionally refuse the call
// when an explicit deny rule matches the original line, so a rewrite never
// launders a denied command (agent.rewriteBashArgs). A rewrite that does
// not parse again, or that changes the line's shape, leaves the call as the
// model sent it.
//
// The note names the rules that fired, which are the user's validated plain
// words, and the line that ran, cleaned to one capped line. It carries no
// other model text.
func RewriteBash(rules []shellsafe.RewriteRule, args map[string]any) (map[string]any, string, bool) {
	cmd, ok := args["command"].(string)
	if !ok || len(rules) == 0 {
		return args, "", false
	}
	out, applied, _ := shellsafe.Rewrite(cmd, rules)
	if len(applied) == 0 || out == cmd {
		return args, "", false
	}
	next := make(map[string]any, len(args))
	for k, v := range args {
		next[k] = v
	}
	next["command"] = out
	seen := map[string]bool{}
	var used []string
	for _, r := range applied {
		if s := r.String(); !seen[s] {
			seen[s] = true
			used = append(used, "`"+s+"`")
		}
	}
	note := fmt.Sprintf("[harness: the user's Bash rewrite rules changed your command before it was checked and run (%s); the command is now: `%s`]\n",
		strings.Join(used, ", "), sanitize.Line(out, 300))
	return next, note, true
}
