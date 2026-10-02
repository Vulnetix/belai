package libitem

import (
	"strconv"
	"strings"
)

// A front-matter document is the dialect the skill loader reads: a "---" line,
// one "key: value" per line, a "---" line, then the body. There is no YAML
// beyond that: no nesting, no multi-line value, no anchors. A key that is not
// declared, a key given twice and a line that is not "key: value" are all
// refusals.

type fmField struct {
	key, val string
}

// splitFrontMatter separates a canonical document into its front-matter lines
// and its body. The body has its leading newlines removed and nothing else.
func splitFrontMatter(doc string) (fields []fmField, body string, err error) {
	if !strings.HasPrefix(doc, "---\n") {
		return nil, "", refuse("the document must start with a --- front-matter line")
	}
	rest := doc[len("---\n"):]
	var fmLines []string
	closed := false
	for {
		line, after, found := strings.Cut(rest, "\n")
		if line == "---" {
			rest, closed = after, true
			break
		}
		if !found {
			break
		}
		fmLines = append(fmLines, line)
		rest = after
	}
	if !closed {
		return nil, "", refuse("the front matter is not closed by a --- line")
	}
	seen := map[string]bool{}
	for _, line := range fmLines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "---") {
			return nil, "", refuse("the front matter has a stray --- line")
		}
		if line != strings.TrimLeft(line, " \t") {
			return nil, "", refuse("front-matter line %q is indented; nested values are not supported", clip(t, 40))
		}
		key, val, ok := strings.Cut(t, ":")
		if !ok {
			return nil, "", refuse("malformed front-matter line %q", clip(t, 40))
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, "", refuse("malformed front-matter line %q", clip(t, 40))
		}
		if seen[key] {
			return nil, "", refuse("front-matter key %q is given twice", clip(key, 40))
		}
		seen[key] = true
		fields = append(fields, fmField{key: key, val: strings.TrimSpace(val)})
	}
	return fields, strings.TrimLeft(rest, "\n"), nil
}

// fmString reads a scalar the way the skill loader does: one pair of matching
// quotes is removed.
func fmString(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

func fmBool(key, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, refuse("%s must be true or false, not %q", key, clip(v, 20))
}

func fmInt(key, v string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, refuse("%s must be a whole number, not %q", key, clip(v, 20))
	}
	if n < lo || n > hi {
		return 0, refuse("%s must be between %d and %d", key, lo, hi)
	}
	return n, nil
}
