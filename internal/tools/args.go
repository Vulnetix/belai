package tools

import (
	"encoding/json"
	"strconv"
	"strings"
)

// argString returns the string value at key. Models on the string-args path
// send every argument as a JSON string, so this is the canonical path for the
// new tools. A non-string value is treated as absent: fail closed rather than
// coercing a number into a path.
//
// A small alias table is consulted only when the canonical key is absent, so
// transcripts and e2e fixtures written against the old `path` spelling keep
// working while the advertised schema names `file_path`. The canonical key
// wins when both are present.
func argString(args map[string]any, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	if s, ok := args[key].(string); ok {
		return s, true
	}
	for _, alias := range argAliases[key] {
		if s, ok := args[alias].(string); ok {
			return s, true
		}
	}
	return "", false
}

// argAliases maps a canonical argument name to its accepted aliases. It is
// deliberately small and symmetric: only the file_path/path rename carries an
// alias, so nothing else silently changes meaning.
var argAliases = map[string][]string{
	"file_path": {"path"},
	"path":      {"file_path"},
}

// argBool returns the boolean value at key. It accepts the forms models
// actually send: true, "true", "1", 1, and 1.0 (OpenAI string-args sends
// booleans as strings; a provider may send a JSON number). Anything else is
// absent.
func argBool(args map[string]any, key string) (bool, bool) {
	if args == nil {
		return false, false
	}
	switch t := args[key].(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1":
			return true, true
		case "false", "0", "":
			return false, true
		}
		return false, false
	case float64:
		return t != 0, true
	case int:
		return t != 0, true
	case int64:
		return t != 0, true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return false, false
		}
		return f != 0, true
	}
	return false, false
}

// argInt64 returns the integer value at key. It accepts the JSON number forms
// (float64, int, int64, json.Number) and a decimal string, matching what Read
// accepted before the coercion was centralised here.
func argInt64(args map[string]any, key string) (int64, bool) {
	if args == nil {
		return 0, false
	}
	switch t := args[key].(type) {
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
