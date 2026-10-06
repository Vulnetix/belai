package libitem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// The JSON kinds are validated over the decoded document as a generic tree, with
// a small set of readers, the same way the library's server does it
// (vdb-site belai_items_validate*.go, the reference): a field of the wrong type,
// including null, is refused, a number that is not a whole number is refused, and
// every key a kind does not name is refused, spelled exactly. A document that
// passes is then decoded into the kind's typed struct, which cannot fail.

// object decodes a canonical JSON document into its top-level object.
func object(canonical []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, refuse("the document must be a JSON object")
	}
	return m, nil
}

// onlyKeys refuses the first key (in sorted order) that is not allowed.
func onlyKeys(m map[string]any, where string, allowed ...string) error {
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	var unknown []string
	for k := range m {
		if !ok[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return refuse("%s: unknown field %q", where, clip(unknown[0], 40))
}

// plainText reports whether s holds no control character at all.
func plainText(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// argText reports whether s may be an argument or a value: no NUL and no control
// character other than tab, line feed and carriage return, so a script passed as
// one argument is possible but a terminal escape is not.
func argText(s string) bool {
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// str reads a string field. A missing field is "" with present false; a field of
// another type (null included) is an error.
func str(m map[string]any, key, where string) (s string, present bool, err error) {
	v, found := m[key]
	if !found {
		return "", false, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return "", true, refuse("%s.%s must be a string", where, key)
	}
	return s, true, nil
}

// reqStr reads a required, non-empty string of at most max bytes with no control
// character.
func reqStr(m map[string]any, key, where string, max int) (string, error) {
	s, present, err := str(m, key, where)
	if err != nil {
		return "", err
	}
	if !present || s == "" {
		return "", refuse("%s.%s is required", where, key)
	}
	return s, checkStr(s, key, where, max, false)
}

// checkStr checks a string's size and characters. text allows the characters of
// argText; otherwise none is a control character.
func checkStr(s, key, where string, max int, text bool) error {
	if len(s) > max {
		return refuse("%s.%s is %d bytes; the most is %d", where, key, len(s), max)
	}
	ok := plainText(s)
	if text {
		ok = argText(s)
	}
	if !ok {
		return refuse("%s.%s holds a control character", where, key)
	}
	return nil
}

// optStr reads an optional string of at most max bytes.
func optStr(m map[string]any, key, where string, max int, text bool) (string, error) {
	s, present, err := str(m, key, where)
	if err != nil || !present {
		return "", err
	}
	return s, checkStr(s, key, where, max, text)
}

// boolean reads an optional boolean, def when it is absent.
func boolean(m map[string]any, key, where string, def bool) (bool, error) {
	v, found := m[key]
	if !found {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, refuse("%s.%s must be true or false", where, key)
	}
	return b, nil
}

// whole reads an optional whole number in [lo, hi], def when it is absent. A
// number with a fraction or an exponent is refused, so every host reads the same.
func whole(m map[string]any, key, where string, lo, hi, def int64) (int64, bool, error) {
	v, found := m[key]
	if !found {
		return def, false, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, true, refuse("%s.%s must be a whole number", where, key)
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0, true, refuse("%s.%s must be a whole number", where, key)
	}
	if i < lo || i > hi {
		return 0, true, refuse("%s.%s must be from %d to %d", where, key, lo, hi)
	}
	return i, true, nil
}

// list reads an optional array of at most max entries.
func list(m map[string]any, key, where string, max int) ([]any, bool, error) {
	v, found := m[key]
	if !found {
		return nil, false, nil
	}
	l, ok := v.([]any)
	if !ok {
		return nil, true, refuse("%s.%s must be a list", where, key)
	}
	if len(l) > max {
		return nil, true, refuse("%s.%s has %d entries; the most is %d", where, key, len(l), max)
	}
	return l, true, nil
}

// obj reads an optional object field.
func obj(m map[string]any, key, where string) (map[string]any, bool, error) {
	v, found := m[key]
	if !found {
		return nil, false, nil
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, true, refuse("%s.%s must be an object", where, key)
	}
	return o, true, nil
}

// entry reads one list element as an object.
func entry(v any, where string, i int) (map[string]any, error) {
	o, ok := v.(map[string]any)
	if !ok {
		return nil, refuse("%s[%d] must be an object", where, i)
	}
	return o, nil
}

// docName reads and checks the document's name against a kind's rule.
func docName(m map[string]any, ok func(string) bool, rule string) (string, error) {
	name, present, err := str(m, "name", "document")
	if err != nil {
		return "", err
	}
	if !present || name == "" {
		return "", refuse("name is required")
	}
	if !ok(name) {
		return "", refuse("name %q is not valid: %s", cleanForMessage(name), rule)
	}
	return name, nil
}

// nameRule is what a name must be, for a message.
const nameRule = "start with a lowercase letter or digit, then lowercase letters, digits, . _ or -, 64 characters at most"

// cleanForMessage keeps a refused value short and on one line inside a message.
func cleanForMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
	return clip(s, 40)
}

func itoa(n int) string { return strconv.Itoa(n) }

func idx(where string, i int) string { return fmt.Sprintf("%s[%d]", where, i) }

// sortedKeys returns a map's keys in byte order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
