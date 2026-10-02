package libitem

import (
	"strings"
)

// A front-matter document is the dialect the skill loader reads (and the server
// validates the same way): a "---" line, one "key: value" per line, a closing
// "---" line (trailing spaces and tabs allowed), then the body. Lines are
// trimmed, blank lines and "#" comments are skipped, and there is no YAML beyond
// that: no nesting, no multi-line value. A key that is not declared, a key given
// twice and a line that is not "key: value" are refusals.

type fmField struct {
	key, val string
}

// splitFrontMatter separates a canonical document into its front-matter fields
// and its body, which is everything after the closing line.
func splitFrontMatter(doc string, allowed map[string]bool) ([]fmField, string, error) {
	rest, ok := strings.CutPrefix(doc, "---\n")
	if !ok {
		return nil, "", refuse("the document must open with a --- front matter line")
	}
	var fields []fmField
	seen := map[string]bool{}
	for {
		line, tail, more := strings.Cut(rest, "\n")
		if strings.TrimRight(line, " \t") == "---" {
			return fields, tail, nil
		}
		if !more {
			return nil, "", refuse("the front matter is not closed by a --- line")
		}
		rest = tail
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, ":")
		if !found {
			return nil, "", refuse("malformed front matter line %q", cleanForMessage(line))
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !allowed[key] {
			return nil, "", refuse("unknown front matter field %q", cleanForMessage(key))
		}
		if seen[key] {
			return nil, "", refuse("front matter field %q appears twice", key)
		}
		seen[key] = true
		fields = append(fields, fmField{key, val})
	}
}

// fmString reads a front-matter string the way the loader does: one pair of
// matching quotes is removed and nothing else is interpreted.
func fmString(v string) string {
	s := strings.TrimSpace(v)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		s = s[1 : len(s)-1]
	}
	return s
}

// fmBool reads true or false, any case.
func fmBool(key, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, refuse("front matter %q must be true or false", key)
}

// fmList reads a one-line list `[a, b]` the way the loader does: items are split
// on commas, an item may be double-quoted, and an empty item is refused.
func fmList(key, v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, refuse("front matter %q must be a list like [a, b]", key)
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return nil, nil
	}
	var out []string
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		if p == "" {
			return nil, refuse("front matter %q has an empty list item", key)
		}
		out = append(out, p)
	}
	return out, nil
}
