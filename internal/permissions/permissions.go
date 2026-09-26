// Package permissions implements the tool permission layer (allow/ask/block
// per tool), adopting the Claude Code permission-settings shape for
// interoperability: allow/ask/deny arrays of rule strings, with "block"
// accepted as an alias for "deny".
package permissions

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/trace"
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
// matches no rule is allowed by default (the agent's registry check still
// rejects unregistered tools before permissions are consulted).
type Settings struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	// Block is accepted as an alias for Deny.
	Block []string `json:"block,omitempty"`
}

// From builds Settings from explicit allow/ask/deny rule slices.
func From(allow, ask, deny []string) Settings {
	return Settings{
		Allow: append([]string{}, allow...),
		Ask:   append([]string{}, ask...),
		Deny:  append([]string{}, deny...),
	}
}

// FromSimple converts a map of tool name -> allow/ask/block into Settings with
// bare-tool-name rules. Unknown decision values are treated as block.
//
// Deprecated: the settings file now uses the structured PermissionRules form;
// FromSimple remains only for legacy flat-map decoding.
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

// ValidateRule reports whether a rule string is well-formed: "Tool" or
// "Tool(spec)" with a non-empty tool name and a compilable glob subject.
func ValidateRule(rule string) error {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return errors.New("permission rule is empty")
	}
	tool, spec, hasSpec := parseRule(rule)
	if tool == "" {
		return fmt.Errorf("rule %q has no tool name", rule)
	}
	if strings.ContainsAny(tool, " \t()") {
		return fmt.Errorf("rule %q has invalid tool name %q", rule, tool)
	}
	if hasSpec {
		if spec == "" {
			return fmt.Errorf("rule %q has an empty subject", rule)
		}
		if _, err := globToRegexp(spec); err != nil {
			return fmt.Errorf("rule %q has an invalid pattern: %w", rule, err)
		}
	}
	return nil
}

var (
	permTraceOnce sync.Once
	permTrace     *trace.Writer
)

// Explain returns the decision for a tool invocation against a rule subject,
// plus the rule that decided it ("" when no rule matched and the default
// allow applies).
func (s Settings) Explain(tool, subject string) (Decision, string) {
	dec, rule := s.explain(tool, subject)
	permTraceOnce.Do(func() { permTrace = trace.Env() })
	if permTrace != nil {
		permTrace.Record(trace.Record{Phase: "permissions", Event: "permission", Verdict: string(dec), Tool: tool, Detail: rule})
	}
	return dec, rule
}

func (s Settings) explain(tool, subject string) (Decision, string) {
	for _, r := range append(append([]string{}, s.Deny...), s.Block...) {
		if matchRule(r, tool, subject) {
			return DecisionBlock, r
		}
	}
	for _, r := range s.Allow {
		if matchRule(r, tool, subject) {
			return DecisionAllow, r
		}
	}
	for _, r := range s.Ask {
		if matchRule(r, tool, subject) {
			return DecisionAsk, r
		}
	}
	// No rule matched: allow by default. The permission_no_match posture gate
	// (agent layer) can restore the legacy fail-closed block.
	return DecisionAllow, ""
}

// Evaluate returns the decision for a tool invocation against a rule subject.
// tool is the tool name (e.g. "Bash"); subject is the specific target the rule
// spec is matched against (e.g. the command or path).
func (s Settings) Evaluate(tool, subject string) Decision {
	d, _ := s.Explain(tool, subject)
	return d
}

// ExplicitlyAllows reports whether a rule the user wrote allows this call.
// Unlike Evaluate it never answers yes by default: it is the question a
// relaxation asks, and a relaxation must be opted into, not fallen into.
func (s Settings) ExplicitlyAllows(tool, subject string) bool {
	for _, r := range s.Allow {
		if matchRule(r, tool, subject) {
			return true
		}
	}
	return false
}

// HasAllowRule reports whether the user wrote any allow rule for the named
// tool, regardless of subject. It is used to decide whether to advertise the
// read-only Bash surface in plan mode.
func (s Settings) HasAllowRule(tool string) bool {
	for _, r := range s.Allow {
		rt, _, _ := parseRule(r)
		if strings.EqualFold(rt, tool) {
			return true
		}
	}
	return false
}

// matchRule matches one rule against a tool name and subject. Tool names are
// matched case-insensitively so a rule written "read" still applies to the
// canonical "Read" tool; subjects remain case-sensitive.
func matchRule(rule, tool, subject string) bool {
	rt, spec, hasSpec := parseRule(rule)
	if !strings.EqualFold(rt, tool) {
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

// globToRegexp converts a glob to a regexp. `*` and `**` match any run
// (crossing path separators); `?` matches exactly one character.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '*':
			// Collapse consecutive stars: ** is the same wildcard as *.
			for i+1 < len(runes) && runes[i+1] == '*' {
				i++
			}
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
