package libitem

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/skills"
)

// Skill limits, shared with the website.
const (
	// MaxDescriptionBytes is the longest skill or prompt description.
	MaxDescriptionBytes = 300
	// MaxSkillMetadataKeys is how many keys a skill's metadata map holds.
	MaxSkillMetadataKeys = 32
	// MaxSkillAllowedTools is how many tools a skill's allowed-tools list names.
	MaxSkillAllowedTools = 64
)

func init() {
	register(Skill, func(c []byte) (string, error) { d, err := ParseSkill(c); return d.Name, err })
}

// SkillDoc is a validated skill: the front-matter fields the host keeps beyond
// the loader's own, and the body.
type SkillDoc struct {
	Name        string
	Description string
	Body        string
}

// ParseSkill validates a canonical skill document. The loader's own validator
// (internal/skills) runs first and is the source of truth for the front-matter
// fields and their shapes; this adds the library's limits on top of it.
func ParseSkill(canonical []byte) (SkillDoc, error) {
	doc := string(canonical)
	fields, body, err := splitFrontMatter(doc)
	if err != nil {
		return SkillDoc{}, err
	}
	m, err := skills.ValidateSkill(doc)
	if err != nil {
		return SkillDoc{}, refuse("%s", clip(err.Error(), 200))
	}
	if !ValidName(Skill, m.Name) {
		return SkillDoc{}, nameError(Skill, m.Name)
	}
	if err := checkDescription(m.Description, true); err != nil {
		return SkillDoc{}, err
	}
	if len(m.AllowedTools) > MaxSkillAllowedTools {
		return SkillDoc{}, refuse("allowed-tools names %d tools; at most %d", len(m.AllowedTools), MaxSkillAllowedTools)
	}
	for _, t := range m.AllowedTools {
		if t == "" || len(t) > 128 || strings.ContainsFunc(t, unicode.IsControl) {
			return SkillDoc{}, refuse("allowed-tools holds a name that is not a tool name")
		}
	}
	for _, f := range fields {
		if f.key == "metadata" {
			if err := checkMetadata(f.val); err != nil {
				return SkillDoc{}, err
			}
		}
	}
	if strings.TrimSpace(body) == "" {
		return SkillDoc{}, refuse("the skill body is empty")
	}
	return SkillDoc{Name: m.Name, Description: m.Description, Body: body}, nil
}

// checkDescription applies the length and character rules of a description.
func checkDescription(d string, required bool) error {
	if d == "" {
		if required {
			return refuse("description is required")
		}
		return nil
	}
	if len(d) > MaxDescriptionBytes {
		return refuse("description is %d bytes; at most %d", len(d), MaxDescriptionBytes)
	}
	if strings.ContainsAny(d, "\n\r") {
		return refuse("description must be one line")
	}
	if !utf8.ValidString(d) || strings.ContainsFunc(d, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return refuse("description holds a control or invisible character")
	}
	return nil
}

// checkMetadata accepts a skill's metadata as a one-line {key: value, ...} map
// of at most MaxSkillMetadataKeys string pairs. The loader reads one front-matter
// line per key, so a nested block map cannot be expressed and is refused.
func checkMetadata(v string) error {
	if !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
		return refuse("metadata must be a one-line {key: value} map")
	}
	inner := strings.TrimSpace(v[1 : len(v)-1])
	if inner == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(inner, ",") {
		k, val, ok := strings.Cut(part, ":")
		k, val = fmString(strings.TrimSpace(k)), fmString(strings.TrimSpace(val))
		if !ok || k == "" {
			return refuse("metadata entry %q is not key: value", clip(strings.TrimSpace(part), 40))
		}
		_ = val
		if seen[k] {
			return refuse("metadata key %q is given twice", clip(k, 40))
		}
		seen[k] = true
	}
	if len(seen) > MaxSkillMetadataKeys {
		return refuse("metadata has %d keys; at most %d", len(seen), MaxSkillMetadataKeys)
	}
	return nil
}
