// Package skills validates Agent Skills front-matter before a skill is ever
// loaded. Skills load only after strict schema validation: required name and
// description, and optional license/compatibility/metadata/allowed-tools/
// disable-model-invocation. Unknown fields are rejected.
package skills

import (
	"fmt"
	"strings"
)

// Manifest is the validated front-matter of a skill.
type Manifest struct {
	Name                   string
	Description            string
	License                string
	Compatibility          string
	Metadata               string
	AllowedTools           []string
	DisableModelInvocation bool
}

var allowedFields = map[string]bool{
	"name":                     true,
	"description":              true,
	"license":                  true,
	"compatibility":            true,
	"metadata":                 true,
	"allowed-tools":            true,
	"disable-model-invocation": true,
}

// ValidateSkill parses and strictly validates a SKILL.md document's
// front-matter. It returns a Manifest on success.
func ValidateSkill(doc string) (*Manifest, error) {
	fm, err := extractFrontMatter(doc)
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed front-matter line %q", line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !allowedFields[key] {
			return nil, fmt.Errorf("unknown front-matter field %q", key)
		}
		var err error
		switch key {
		case "name":
			m.Name, err = parseString(val)
		case "description":
			m.Description, err = parseString(val)
		case "license":
			m.License, err = parseString(val)
		case "compatibility":
			m.Compatibility, err = parseString(val)
		case "metadata":
			m.Metadata, err = parseString(val)
		case "allowed-tools":
			m.AllowedTools, err = parseList(val)
		case "disable-model-invocation":
			m.DisableModelInvocation, err = parseBool(val)
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("required front-matter field %q missing or empty", "name")
	}
	if strings.TrimSpace(m.Description) == "" {
		return nil, fmt.Errorf("required front-matter field %q missing or empty", "description")
	}
	return m, nil
}

// extractFrontMatter returns the text between the leading "---" and the next
// "---" line.
func extractFrontMatter(doc string) (string, error) {
	if !strings.HasPrefix(doc, "---\n") {
		return "", fmt.Errorf("missing front-matter delimiter ---")
	}
	rest := doc[len("---\n"):]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", fmt.Errorf("unterminated front-matter")
	}
	return rest[:idx], nil
}

func parseString(v string) (string, error) {
	s := strings.TrimSpace(v)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	return s, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected true/false, got %q", v)
	}
}

func parseList(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, fmt.Errorf("expected list [a, b], got %q", v)
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return nil, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && (p[0] == '"' && p[len(p)-1] == '"') {
			p = p[1 : len(p)-1]
		}
		if p == "" {
			return nil, fmt.Errorf("empty list item in %q", v)
		}
		out = append(out, p)
	}
	return out, nil
}
