package agentimport

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// doc is a decoded YAML or JSON mapping.
type doc map[string]any

func parseDoc(data []byte) (doc, error) {
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("not valid YAML or JSON: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("the file holds no mapping")
	}
	return doc(m), nil
}

// sub returns the nested mapping at key, or nil.
func (d doc) sub(keys ...string) doc {
	cur := d
	for _, k := range keys {
		if cur == nil {
			return nil
		}
		switch v := cur[k].(type) {
		case map[string]any:
			cur = doc(v)
		default:
			return nil
		}
	}
	return cur
}

// str returns the string at key; numbers and booleans are written out.
func (d doc) str(key string) string {
	if d == nil {
		return ""
	}
	switch v := d[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case int, int64, uint64, float64, bool:
		return fmt.Sprint(v)
	}
	return ""
}

// strs returns the strings of the list at key (a lone string is a list of one).
func (d doc) strs(key string) []string {
	if d == nil {
		return nil
	}
	switch v := d[key].(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// num returns the whole number at key, or 0.
func (d doc) num(key string) int {
	if d == nil {
		return 0
	}
	switch v := d[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// keys returns the mapping's keys, sorted.
func (d doc) keys() []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// frontMatter splits a markdown document into its YAML front matter and body.
func frontMatter(text string) (doc, string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, "", fmt.Errorf("the file does not start with a --- front-matter line")
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, "", fmt.Errorf("the front matter is not closed by a --- line")
	}
	d, err := parseDoc([]byte(rest[:end]))
	if err != nil {
		return nil, "", err
	}
	body := rest[end+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return d, strings.TrimSpace(body), nil
}
