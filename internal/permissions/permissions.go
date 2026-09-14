// Package permissions implements the tool permission layer (allow/ask/block
// per tool), adopting the Claude Code permission-settings shape for
// interoperability: allow/ask/deny arrays of rule strings, with "block"
// accepted as an alias for "deny".
package permissions

import (
	"regexp"
	"strings"
)

// Decision is the permission decision for a tool invocation.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionBlock Decision = "block"
)

// Settings is the permission configuration. Rules are either a bare tool name
// ("Read") or a tool name plus a glob spec ("Bash(git diff:*)",
// "Read(./src/**)"). Deny (or Block) wins over Allow and Ask; a tool that
// matches no rule is blocked (unknown tools are never forwarded).
type Settings struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	// Block is accepted as an alias for Deny.
	Block []string `json:"block,omitempty"`
}

// FromSimple converts a map of tool name -> allow/ask/block into Settings with
// bare-tool-name rules. Unknown decision values are treated as block.
func FromSimple(m map[string]string) Settings {
	var s Settings
	for tool, d := range m {
		switch strings.ToLower(strings.TrimSpace(d)) {
		case "allow":
			s.Allow = append(s.Allow, tool)
		case "ask":
			s.Ask = append(s.Ask, tool)
		default:
			s.Deny = append(s.Deny, tool)
		}
	}
	return s
}

// Evaluate returns the decision for a tool invocation against a rule subject.
// tool is the tool name (e.g. "Bash"); subject is the specific target the rule
// spec is matched against (e.g. the command or path).
func (s Settings) Evaluate(tool, subject string) Decision {
	for _, r := range append(append([]string{}, s.Deny...), s.Block...) {
		if matchRule(r, tool, subject) {
			return DecisionBlock
		}
	}
	for _, r := range s.Allow {
		if matchRule(r, tool, subject) {
			return DecisionAllow
		}
	}
	for _, r := range s.Ask {
		if matchRule(r, tool, subject) {
			return DecisionAsk
		}
	}
	return DecisionBlock
}

// matchRule matches one rule against a tool name and subject.
func matchRule(rule, tool, subject string) bool {
	rt, spec, hasSpec := parseRule(rule)
	if rt != tool {
		return false
	}
	if !hasSpec {
		return true
	}
	re, err := globToRegexp(spec)
	if err != nil {
		return false
	}
	return re.MatchString(subject)
}

// parseRule splits "Tool(spec)" into its parts.
func parseRule(rule string) (tool, spec string, hasSpec bool) {
	i := strings.Index(rule, "(")
	if i >= 0 && strings.HasSuffix(rule, ")") {
		return rule[:i], rule[i+1 : len(rule)-1], true
	}
	return rule, "", false
}

// globToRegexp converts a glob (`*` matches any run, `?` matches one run) to a
// regexp. Unlike path.Match, `*` crosses path separators.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
