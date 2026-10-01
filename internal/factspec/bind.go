package factspec

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// alwaysRefused are flags that point a tool's credentials at another
// endpoint, identity or trust root. The credentials a call carries (ambient
// or assumed) attach to every request, so these are refused whether or not a
// profile declares facts.
var alwaysRefused = map[string][]string{
	ToolAWS: {"--profile", "--endpoint-url", "--ca-bundle", "--no-verify-ssl", "--no-sign-request"},
	ToolKubectl: {
		"--kubeconfig", "--server", "-s", "--token", "--as", "--as-group", "--as-uid",
		"--certificate-authority", "--client-certificate", "--client-key",
		"--insecure-skip-tls-verify", "--username", "--password", "--tls-server-name", "--proxy-url",
	},
	ToolGCloud: {"--impersonate-service-account", "--access-token-file"},
}

// Applied is what a tool's facts add to one call.
type Applied struct {
	// Env is appended to the tool's scrubbed environment.
	Env []string
	// Front goes before the subcommand, Back after the command's own
	// arguments.
	Front, Back []string
}

// Refused is returned for a flag the call may not carry.
type Refused struct {
	Flag, Why string
}

func (r *Refused) Error() string { return fmt.Sprintf("%s is not allowed: %s", r.Flag, r.Why) }

// Bind returns what facts add to a call of tool whose command is argv (the
// tokens after the binary's name). It refuses a call that carries a flag the
// harness never allows or that a pinned fact forbids. A nil facts map still
// applies the flags that are always refused.
func Bind(tool string, facts map[string][]string, argv []string) (Applied, error) {
	var out Applied
	for _, f := range alwaysRefused[tool] {
		if hasFlag(argv, f) {
			return Applied{}, &Refused{Flag: f, Why: "it can send this tool's credentials to another endpoint or identity"}
		}
	}
	for _, e := range table {
		keys := keysFor(e, facts)
		if len(keys) == 0 {
			continue
		}
		if e.Pin {
			for _, o := range e.Overrides {
				if o.Tool != tool {
					continue
				}
				for _, f := range o.Flags {
					if hasFlag(argv, f) {
						return Applied{}, &Refused{Flag: f, Why: "the profile pins " + e.Key}
					}
				}
			}
		}
		for _, b := range e.Env {
			if b.Tool != tool {
				continue
			}
			for _, k := range keys {
				v := ExpandHome(strings.Join(facts[k], ","))
				if e.Family {
					v = varValue(facts[k])
				}
				names := b.Names
				if e.Family {
					names = []string{"TF_VAR_" + strings.TrimPrefix(k, e.Key)}
				}
				for _, n := range names {
					out.Env = append(out.Env, n+"="+v)
				}
			}
		}
		for _, fb := range e.Flags {
			if fb.Tool != tool || (fb.Skip != nil && fb.Skip(argv)) {
				continue
			}
			if hasFlag(argv, fb.Flag) {
				continue
			}
			chosen := false
			for _, u := range fb.Unless {
				if hasFlag(argv, u) {
					chosen = true
					break
				}
			}
			if chosen {
				continue
			}
			v := facts[keys[0]][0]
			var toks []string
			if fb.Joined {
				toks = []string{fb.Flag + "=" + v}
			} else {
				toks = []string{fb.Flag, v}
			}
			if fb.Front {
				out.Front = append(out.Front, toks...)
			} else {
				out.Back = append(out.Back, toks...)
			}
		}
	}
	return out, nil
}

// keysFor returns the keys of facts that e covers.
func keysFor(e Entry, facts map[string][]string) []string {
	if !e.Family {
		if len(facts[e.Key]) > 0 {
			return []string{e.Key}
		}
		return nil
	}
	var keys []string
	for _, k := range sortedKeys(facts) {
		if strings.HasPrefix(k, e.Key) && len(k) > len(e.Key) {
			keys = append(keys, k)
		}
	}
	return keys
}

// varValue renders a fact as a Terraform variable's environment value: one
// value as written, several as a JSON array.
func varValue(vals []string) string {
	if len(vals) == 1 {
		return vals[0]
	}
	b, _ := json.Marshal(vals)
	return string(b)
}

// HasFlag reports whether argv carries flag, in any form a CLI may read it.
func HasFlag(argv []string, flag string) bool { return hasFlag(argv, flag) }

// hasFlag reports whether argv carries flag. A long flag also matches its
// abbreviations and its --flag=value form, since a CLI may accept either; a
// short flag matches when it is attached to a value (-nfoo, -n=foo).
func hasFlag(argv []string, flag string) bool {
	for _, tok := range argv {
		if tok == "--" {
			return false
		}
		if matchFlag(tok, flag) {
			return true
		}
	}
	return false
}

func matchFlag(tok, flag string) bool {
	switch {
	case strings.HasPrefix(flag, "--"):
		if !strings.HasPrefix(tok, "--") {
			return false
		}
		name, _, _ := strings.Cut(tok[2:], "=")
		return name != "" && strings.HasPrefix(flag[2:], name)
	case len(flag) == 2:
		return !strings.HasPrefix(tok, "--") && strings.HasPrefix(tok, flag)
	default:
		// A single-dash word (terraform -chdir), which Go's flag parsing also
		// reads with two dashes.
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-"), "=")
		return strings.HasPrefix(tok, "-") && name == flag[1:]
	}
}

// AlwaysRefused returns the flags refused for tool whether or not a profile
// declares facts, in table order. The documentation lists each of them, and a
// test holds the two together.
func AlwaysRefused(tool string) []string { return append([]string(nil), alwaysRefused[tool]...) }

// RefusedTools returns the tools that refuse flags, in name order.
func RefusedTools() []string {
	out := make([]string, 0, len(alwaysRefused))
	for t := range alwaysRefused {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// RefusedMarkdown renders the always-refused flags as the documentation's
// table, which docs/agent-profiles.md carries between marker comments.
func RefusedMarkdown() string {
	var b strings.Builder
	b.WriteString("| Tool | Flags always refused |\n| ---- | -------------------- |\n")
	for _, t := range RefusedTools() {
		flags := make([]string, 0, len(alwaysRefused[t]))
		for _, f := range alwaysRefused[t] {
			flags = append(flags, "`"+f+"`")
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", t, strings.Join(flags, ", "))
	}
	return b.String()
}
