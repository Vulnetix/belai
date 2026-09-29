package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// toleratedArgs are keys a tool accepts without advertising them: trained
// arguments that are advisory for this harness, so ignoring them cannot give
// the model a different answer than it asked for. Empty today: Bash's
// description and timeout are now declared arguments.
var toleratedArgs = map[string][]string{}

// CheckArgs refuses argument keys the tool's schema does not declare, and
// then checks each declared value against its schema (type, enum, format).
//
// Keys: refuses argument keys the tool's schema does not declare. A key
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
		return checkValues(def, args)
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

// checkValues validates each declared argument that is present. A value of the
// wrong JSON type, outside its enum, or failing its declared Format is
// refused with a message naming the argument, so the model can correct the
// call. Numbers and booleans sent as strings are accepted, as the argument
// readers accept them. Missing arguments are left to the tool.
func checkValues(def Definition, args map[string]any) error {
	keys := make([]string, 0, len(def.Properties))
	for k := range def.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prop := def.Properties[k]
		v, ok := args[k]
		if !ok {
			for _, alias := range argAliases[k] {
				if v, ok = args[alias]; ok {
					break
				}
			}
		}
		if !ok || v == nil {
			continue
		}
		if err := checkType(prop, v); err != nil {
			return fmt.Errorf("%s: argument %q %w", def.Name, k, err)
		}
		s, isString := v.(string)
		if !isString {
			continue
		}
		if len(prop.Enum) > 0 && s != "" && !containsString(prop.Enum, s) {
			return fmt.Errorf("%s: argument %q must be one of %s", def.Name, k, strings.Join(prop.Enum, ", "))
		}
		if err := CheckFormat(prop.Format, s); err != nil {
			return fmt.Errorf("%s: argument %q is not a valid %s: %v", def.Name, k, prop.Format, err)
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// checkType reports a value whose JSON type does not fit the property.
func checkType(prop Property, v any) error {
	switch prop.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("must be a string")
		}
	case "integer":
		switch n := v.(type) {
		case float64:
			if n != float64(int64(n)) {
				return fmt.Errorf("must be a whole number")
			}
		case int, int32, int64, json.Number:
		case string:
			if _, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err != nil {
				return fmt.Errorf("must be a whole number")
			}
		default:
			return fmt.Errorf("must be a whole number")
		}
	case "number":
		switch n := v.(type) {
		case float64, int, int32, int64, json.Number:
		case string:
			if _, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err != nil {
				return fmt.Errorf("must be a number")
			}
		default:
			return fmt.Errorf("must be a number")
		}
	case "boolean":
		switch b := v.(type) {
		case bool:
		case string:
			if l := strings.ToLower(strings.TrimSpace(b)); l != "true" && l != "false" {
				return fmt.Errorf("must be true or false")
			}
		default:
			return fmt.Errorf("must be true or false")
		}
	case "array":
		switch v.(type) {
		case []any, []string:
		default:
			return fmt.Errorf("must be an array")
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("must be an object")
		}
	}
	return nil
}
