package libitem

import (
	"strings"

	"github.com/vulnetix/belai/internal/skills"
)

// Skill limits, shared with the website and the server.
const (
	// MaxDescriptionBytes is the longest prompt description.
	MaxDescriptionBytes = 300
	// MaxSkillDescriptionBytes is the longest skill description, the Agent Skills
	// specification's limit.
	MaxSkillDescriptionBytes = 1024
	// MaxSkillLicense and MaxSkillCompatibility bound those front-matter values,
	// in bytes.
	MaxSkillLicense       = 128
	MaxSkillCompatibility = 500
	// A skill's metadata is the specification's map of string keys to string
	// values, bounded by skills.MaxMetadataEntries, MaxMetadataKey,
	// MaxMetadataValue and MaxMetadataTotal.
	//
	// MaxSkillAllowedTools is how many tools a skill's allowed-tools names, and
	// MaxSkillTool the longest tool name.
	MaxSkillAllowedTools = 64
	MaxSkillTool         = 128
)

func init() {
	register(Skill,
		func(c []byte) (string, error) { d, err := ParseSkill(c); return d.Name, err },
		// ParseSkill already ran the loader's own validator, which is the source of
		// truth for the front matter; a skill it would not load is never written.
		func(c []byte) error {
			if _, err := skills.ValidateSkill(string(c)); err != nil {
				return refuse("%s", clip(err.Error(), 200))
			}
			return nil
		})
}

// SkillDoc is a validated skill: its name and description, and the instructions
// after the front matter.
type SkillDoc struct {
	Name        string
	Description string
	Body        string
}

// ParseSkill validates a canonical SKILL.md document against the library's rules.
func ParseSkill(canonical []byte) (SkillDoc, error) {
	doc := string(canonical)
	if !strings.HasPrefix(doc, "---\n") {
		return SkillDoc{}, refuse("the document must open with a --- front matter line")
	}
	if err := strictClosing(doc); err != nil {
		return SkillDoc{}, err
	}
	m, err := skills.ValidateSkill(doc)
	if err != nil {
		return SkillDoc{}, refuse("%s", clip(err.Error(), 200))
	}
	var d SkillDoc
	d.Name = m.Name
	if !ValidName(Skill, d.Name) {
		return d, refuse("name %q is not valid: %s", cleanForMessage(d.Name), nameRule)
	}
	if skills.ReservedName(d.Name) {
		return d, refuse("names starting with %q are reserved for Belai's own skills", skills.BuiltinPrefix)
	}
	d.Description = m.Description
	if len(d.Description) > MaxSkillDescriptionBytes {
		return d, refuse("description is %d bytes; the most is %d", len(d.Description), MaxSkillDescriptionBytes)
	}
	if len(m.License) > MaxSkillLicense {
		return d, refuse("license is over %d bytes", MaxSkillLicense)
	}
	if len(m.Compatibility) > MaxSkillCompatibility {
		return d, refuse("compatibility is over %d bytes", MaxSkillCompatibility)
	}
	if err := skills.CheckMetadataBounds(m.Metadata); err != nil {
		return d, refuse("%s", err.Error())
	}
	if len(m.AllowedTools) > MaxSkillAllowedTools {
		return d, refuse("allowed-tools has %d entries; the most is %d", len(m.AllowedTools), MaxSkillAllowedTools)
	}
	for _, t := range m.AllowedTools {
		if len(t) > MaxSkillTool {
			return d, refuse("an allowed-tools entry is over %d bytes", MaxSkillTool)
		}
	}
	_, body, err := skills.Split(doc)
	if err != nil {
		return d, refuse("%s", clip(err.Error(), 200))
	}
	if strings.TrimSpace(body) == "" {
		return d, refuse("the skill has no instructions after the front matter")
	}
	d.Body = strings.TrimLeft(body, "\n")
	return d, nil
}

// strictClosing requires the front matter's closing line to be exactly "---"
// (trailing spaces and tabs allowed). The loader accepts any line that starts
// with it, so a longer line is the library's refusal and not the loader's.
func strictClosing(doc string) error {
	rest := strings.TrimPrefix(doc, "---\n")
	for {
		line, tail, more := strings.Cut(rest, "\n")
		if strings.HasPrefix(line, "---") {
			if strings.TrimRight(line, " \t") != "---" {
				return refuse("malformed front matter line %q", cleanForMessage(strings.TrimSpace(line)))
			}
			return nil
		}
		if !more {
			return refuse("the front matter is not closed by a --- line")
		}
		rest = tail
	}
}
