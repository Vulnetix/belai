// Package skills validates Agent Skills front-matter before a skill is ever
// loaded. Skills load only after strict schema validation: required name and
// description, and optional license/compatibility/metadata/allowed-tools/
// disable-model-invocation. Unknown fields are rejected.
//
// The format is the Agent Skills specification (agentskills.io): metadata is a
// map of string keys to string values and allowed-tools is a space-separated
// string. Files written for the earlier loader still load: a front matter that
// is not valid YAML (for example `description: Cut a release: tag`) is read
// line by line, a scalar metadata value is kept under the key "note", and
// allowed-tools may still be a `[a, b]` list.
package skills

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the validated front-matter of a skill.
type Manifest struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	// Metadata is the spec's string-to-string map. A scalar value in a file
	// written for the earlier loader is held under the key MetadataNote.
	Metadata               map[string]string
	AllowedTools           []string
	DisableModelInvocation bool
}

// MetadataNote is the key a legacy scalar `metadata: text` is kept under.
const MetadataNote = "note"

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
	m, err := parseFrontMatter(fm)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("required front-matter field %q missing or empty", "name")
	}
	if strings.TrimSpace(m.Description) == "" {
		return nil, fmt.Errorf("required front-matter field %q missing or empty", "description")
	}
	if err := validateBelaiMetadata(m.Metadata); err != nil {
		return nil, fmt.Errorf("field %q: %w", "metadata", err)
	}
	return m, nil
}

// Split returns the text of the front matter and the body after it, or an
// error when the document has no well-formed front matter.
func Split(doc string) (frontMatter, body string, err error) {
	return splitDoc(doc)
}

// extractFrontMatter returns the text between the leading "---" and the next
// "---" line.
func extractFrontMatter(doc string) (string, error) {
	fm, _, err := splitDoc(doc)
	return fm, err
}

// splitDoc separates a document into its front matter and its body. The
// closing delimiter is the first line that starts with "---"; the rest of that
// line goes with it. Only line breaks are trimmed from the start of the body, so
// a body that opens with a list marker or a rule keeps it.
func splitDoc(doc string) (string, string, error) {
	rest, ok := strings.CutPrefix(doc, "---\n")
	if !ok {
		return "", "", fmt.Errorf("missing front-matter delimiter ---")
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("unterminated front-matter")
	}
	body := rest[idx+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return rest[:idx], strings.TrimLeft(body, "\r\n"), nil
}

// parseFrontMatter reads the front matter as YAML when it is YAML, and line by
// line when it is not. The decision is syntax only: every semantic rule (known
// fields, duplicate keys, value types) is applied by both readers.
func parseFrontMatter(fm string) (*Manifest, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &root); err != nil {
		return parseLegacy(fm)
	}
	if root.Kind == 0 {
		return &Manifest{}, nil
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return parseLegacy(fm)
	}
	return parseMapping(root.Content[0])
}

func parseMapping(n *yaml.Node) (*Manifest, error) {
	m := &Manifest{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		kn, vn := n.Content[i], n.Content[i+1]
		if kn.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("front-matter keys must be plain words")
		}
		key := kn.Value
		if !allowedFields[key] {
			return nil, fmt.Errorf("unknown front-matter field %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("front-matter field %q appears twice", key)
		}
		seen[key] = true
		var err error
		switch key {
		case "name":
			m.Name, err = scalarString(vn)
		case "description":
			m.Description, err = scalarString(vn)
		case "license":
			m.License, err = scalarString(vn)
		case "compatibility":
			m.Compatibility, err = scalarString(vn)
		case "metadata":
			m.Metadata, err = nodeMetadata(vn)
		case "allowed-tools":
			m.AllowedTools, err = nodeTools(vn)
		case "disable-model-invocation":
			var s string
			if s, err = scalarString(vn); err == nil {
				m.DisableModelInvocation, err = parseBool(s)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
	}
	return m, nil
}

func scalarString(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("expected a single value")
	}
	if n.Tag == "!!null" {
		return "", nil
	}
	return strings.TrimSpace(n.Value), nil
}

func nodeMetadata(n *yaml.Node) (map[string]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		s, _ := scalarString(n)
		if s == "" {
			return nil, nil
		}
		return map[string]string{MetadataNote: s}, nil
	case yaml.MappingNode:
		out := map[string]string{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			kn, vn := n.Content[i], n.Content[i+1]
			if kn.Kind != yaml.ScalarNode || vn.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("expected a map of string keys to string values")
			}
			if _, dup := out[kn.Value]; dup {
				return nil, fmt.Errorf("key %q appears twice", kn.Value)
			}
			v, _ := scalarString(vn)
			out[kn.Value] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected a map of string keys to string values")
}

func nodeTools(n *yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		s, _ := scalarString(n)
		return splitTools(s), nil
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("expected a list of tool names")
			}
			s, _ := scalarString(c)
			if s == "" {
				return nil, fmt.Errorf("empty list item")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected tool names separated by spaces")
}

// splitTools splits a space-separated tool string. Spaces inside parentheses
// belong to the entry, as in `Bash(git log:*)`.
func splitTools(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '(':
			depth++
			cur.WriteRune(r)
		case r == ')':
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n') && depth == 0:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// parseLegacy reads the front matter line by line, the way the earlier loader
// did: `key: value`, the value taken verbatim after the first colon.
func parseLegacy(fm string) (*Manifest, error) {
	m := &Manifest{}
	seen := map[string]bool{}
	lines := strings.Split(fm, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
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
		if seen[key] {
			return nil, fmt.Errorf("front-matter field %q appears twice", key)
		}
		seen[key] = true
		var err error
		switch key {
		case "name":
			m.Name = parseString(val)
		case "description":
			m.Description = parseString(val)
		case "license":
			m.License = parseString(val)
		case "compatibility":
			m.Compatibility = parseString(val)
		case "metadata":
			if s := parseString(val); s != "" {
				m.Metadata = map[string]string{MetadataNote: s}
			}
		case "allowed-tools":
			m.AllowedTools, err = parseList(val)
		case "disable-model-invocation":
			m.DisableModelInvocation, err = parseBool(val)
		}
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
	}
	return m, nil
}

func parseString(v string) string {
	s := strings.TrimSpace(v)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = s[1 : len(s)-1]
		}
	}
	return s
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

// parseList reads allowed-tools in the legacy `[a, b]` form, or as the spec's
// space-separated string.
func parseList(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") && !strings.HasSuffix(v, "]") {
		return splitTools(v), nil
	}
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

// Fields returns every front-matter field a SKILL.md may use, sorted.
func Fields() []string {
	out := make([]string, 0, len(allowedFields))
	for f := range allowedFields {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
