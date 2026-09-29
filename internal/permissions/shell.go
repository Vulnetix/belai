package permissions

import (
	"strings"

	"github.com/vulnetix/belai/internal/shellsafe"
)

// bashTool is the tool whose subject is a shell command line.
const bashTool = "Bash"

func isShell(tool string) bool { return strings.EqualFold(tool, bashTool) }

// explainShell decides a Bash call by what the command line contains, not by
// the raw string alone.
//
//   - A deny rule fires when it matches the line as written or any command
//     inside it: with quotes removed, with a program path reduced to its name,
//     with git's global options removed, and inside env, sudo, xargs, sh -c
//     and find -exec. "git push" is therefore denied however it is spelled.
//   - An allow rule approves the line only when it covers every command in it,
//     and the line as written also matches an allow rule. `git status*` does
//     not approve `git status; rm -rf x`. A line that did not parse, that has
//     a payload the parser could not see into, or that writes through a
//     redirection is never approved by a rule; it falls through to asking.
//   - An ask rule fires on the line or any command in it.
//
// The order is the package's usual one: deny, allow, ask, then no match.
func (s Settings) explainShell(tool, raw string) (Decision, string) {
	an := shellsafe.Analyze(raw)
	deny := append([]string{raw}, an.DenySubjects()...)
	for _, r := range append(append([]string{}, s.Deny...), s.Block...) {
		if matchAny(r, tool, deny) {
			return DecisionBlock, r
		}
	}
	if rule := s.shellAllowRule(tool, raw, an); rule != "" {
		return DecisionAllow, rule
	}
	parts := append([]string{raw}, an.Subjects()...)
	for _, r := range s.Ask {
		if matchAny(r, tool, parts) {
			return DecisionAsk, r
		}
	}
	return DecisionAllow, ""
}

// shellAllowRule returns the allow rule that approves the whole line, or "".
func (s Settings) shellAllowRule(tool, raw string, an *shellsafe.Analysis) string {
	if !an.Parseable || len(an.Commands) == 0 ||
		an.Has(shellsafe.FlagOpaque|shellsafe.FlagWriteRedirect) {
		return ""
	}
	for _, c := range an.Subjects() {
		if !s.anyAllow(tool, c) {
			return ""
		}
	}
	for _, r := range s.Allow {
		if matchRule(r, tool, raw) {
			return r
		}
	}
	return ""
}

func (s Settings) anyAllow(tool, subject string) bool {
	for _, r := range s.Allow {
		if matchRule(r, tool, subject) {
			return true
		}
	}
	return false
}

func matchAny(rule, tool string, subjects []string) bool {
	for _, sub := range subjects {
		if matchRule(rule, tool, sub) {
			return true
		}
	}
	return false
}
