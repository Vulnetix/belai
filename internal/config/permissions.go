package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PermissionRules is the settings-file form of tool permissions. The current
// structured form is allow/ask/deny arrays of rule strings; the legacy form was
// a flat map of tool name to "allow"/"ask"/"block". Files written today never
// emit the legacy form.
type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// IsZero reports whether no rules are present.
func (p PermissionRules) IsZero() bool {
	return len(p.Allow) == 0 && len(p.Ask) == 0 && len(p.Deny) == 0
}

// Merge unions other into p, preserving order and dropping duplicates. The
// merge is union — never replacement — so a project file can add rules but can
// never remove a rule the user set globally. Evaluation checks deny first, so
// a deny from either side still wins.
func (p PermissionRules) Merge(other PermissionRules) PermissionRules {
	out := PermissionRules{
		Allow: unionStrings(p.Allow, other.Allow),
		Ask:   unionStrings(p.Ask, other.Ask),
		Deny:  unionStrings(p.Deny, other.Deny),
	}
	return out
}

func unionStrings(a, b []string) []string {
	if len(a)+len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// UnmarshalJSON accepts either the legacy flat map (every value a string) or
// the structured form (every value an array). Mixed forms, unknown buckets,
// and any other value type are a decode error: a malformed permissions block
// fails closed loudly rather than silently enabling or blocking tools.
func (p *PermissionRules) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = PermissionRules{}

	arraySeen := false
	stringSeen := false
	for _, v := range raw {
		s := strings.TrimSpace(string(v))
		switch {
		case strings.HasPrefix(s, "["):
			arraySeen = true
		case strings.HasPrefix(s, "\"") || s == "null":
			stringSeen = true
		default:
			return fmt.Errorf("permissions: unsupported rule value %s", s)
		}
	}
	switch {
	case arraySeen && stringSeen:
		return fmt.Errorf("permissions: cannot mix legacy string rules and structured array rules")
	case arraySeen:
		return p.unmarshalStructured(raw)
	default:
		return p.unmarshalLegacy(raw)
	}
}

func (p *PermissionRules) unmarshalStructured(raw map[string]json.RawMessage) error {
	for k := range raw {
		switch k {
		case "allow", "ask", "deny", "block":
		default:
			return fmt.Errorf("permissions: unknown bucket %q", k)
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var tmp struct {
		Allow []string `json:"allow"`
		Ask   []string `json:"ask"`
		Deny  []string `json:"deny"`
		Block []string `json:"block"`
	}
	if err := json.Unmarshal(b, &tmp); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	p.Allow = dedupeStrings(tmp.Allow)
	p.Ask = dedupeStrings(tmp.Ask)
	p.Deny = dedupeStrings(append(tmp.Deny, tmp.Block...))
	return nil
}

func (p *PermissionRules) unmarshalLegacy(raw map[string]json.RawMessage) error {
	// Legacy flat map: tool name -> "allow" | "ask" | anything else (deny),
	// mirroring permissions.FromSimple semantics.
	for tool, v := range raw {
		var decision string
		if err := json.Unmarshal(v, &decision); err != nil {
			return fmt.Errorf("permissions: legacy rule for %q is not a string: %w", tool, err)
		}
		switch strings.ToLower(strings.TrimSpace(decision)) {
		case "allow":
			p.Allow = append(p.Allow, tool)
		case "ask":
			p.Ask = append(p.Ask, tool)
		default:
			p.Deny = append(p.Deny, tool)
		}
	}
	return nil
}

// MarshalJSON emits the structured form with sorted slices. A zero-value rules
// set marshals as an empty object so it can be treated as "unset".
func (p PermissionRules) MarshalJSON() ([]byte, error) {
	if p.IsZero() {
		return []byte("{}"), nil
	}
	type alias PermissionRules
	a := alias(p)
	a.Allow = sortedDedupe(a.Allow)
	a.Ask = sortedDedupe(a.Ask)
	a.Deny = sortedDedupe(a.Deny)
	return json.Marshal(a)
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func sortedDedupe(in []string) []string {
	out := dedupeStrings(in)
	sort.Strings(out)
	return out
}
