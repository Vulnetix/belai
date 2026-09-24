package tools

import (
	"fmt"
	"sort"
	"strings"
)

// toleratedArgs are keys a tool accepts without advertising them: trained
// arguments that are advisory for this harness, so ignoring them cannot give
// the model a different answer than it asked for. Empty today: Bash's
// description and timeout are now declared arguments.
var toleratedArgs = map[string][]string{}

// CheckArgs refuses argument keys the tool's schema does not declare. A key
// the tool silently ignored — Grep's trained `-i` or `glob`, Bash's
// `run_in_background` — would return a different result than the model asked
// for with nothing to say so; naming it lets the model correct the call. The
// file_path/path alias pair is accepted wherever either spelling is declared,
// and a tool that declares no properties at all is not checked.
func CheckArgs(def Definition, args map[string]any) error {
	if len(def.Properties) == 0 {
		return nil
	}
	var unknown []string
	for k := range args {
		if argDeclared(def, k) {
			continue
		}
		unknown = append(unknown, k)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	accepted := make([]string, 0, len(def.Properties))
	for k := range def.Properties {
		accepted = append(accepted, k)
	}
	sort.Strings(accepted)
	return fmt.Errorf("%s does not accept %s; accepted arguments: %s — call it again without them",
		def.Name, quoteList(unknown), strings.Join(accepted, ", "))
}

func argDeclared(def Definition, key string) bool {
	if _, ok := def.Properties[key]; ok {
		return true
	}
	for canonical, aliases := range argAliases {
		if _, ok := def.Properties[canonical]; !ok {
			continue
		}
		for _, a := range aliases {
			if a == key {
				return true
			}
		}
	}
	for _, k := range toleratedArgs[def.Name] {
		if k == key {
			return true
		}
	}
	return false
}

func quoteList(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = fmt.Sprintf("%q", k)
	}
	return strings.Join(q, ", ")
}
