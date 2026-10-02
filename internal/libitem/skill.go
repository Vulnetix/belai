package libitem

import (
	"strings"

	"github.com/vulnetix/belai/internal/skills"
)

// Skill limits, shared with the website and the server.
const (
	// MaxDescriptionBytes is the longest skill or prompt description.
	MaxDescriptionBytes = 300
	// MaxSkillLicense, MaxSkillCompatibility and MaxSkillMetadata bound those
	// front-matter values, in bytes. metadata is the one-line string the loader
	// reads: its parser has no nesting, so a map cannot be expressed.
	MaxSkillLicense       = 128
	MaxSkillCompatibility = 500
	MaxSkillMetadata      = 1024
	// MaxSkillAllowedTools is how many tools a skill's allowed-tools list names,
	// and MaxSkillTool the longest tool name.
	MaxSkillAllowedTools = 64
	MaxSkillTool         = 128
)

var skillFields = map[string]bool{
	"name": true, "description": true, "license": true, "compatibility": true, "metadata": true,
	"allowed-tools": true, "disable-model-invocation": true,
}

func init() {
	register(Skill,
		func(c []byte) (string, error) { d, err := ParseSkill(c); return d.Name, err },
		// The loader's own validator is the source of truth for the front matter; a
		// skill it would not load is never written.
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
	fields, body, err := splitFrontMatter(string(canonical), skillFields)
	if err != nil {
		return SkillDoc{}, err
	}
	var d SkillDoc
	haveName, haveDesc := false, false
	for _, f := range fields {
		switch f.key {
		case "name":
			d.Name, haveName = fmString(f.val), true
			if d.Name == "" {
				return d, refuse("name is required")
			}
			if !ValidName(Skill, d.Name) {
				return d, refuse("name %q is not valid: %s", cleanForMessage(d.Name), nameRule)
			}
		case "description":
			d.Description, haveDesc = fmString(f.val), true
			if d.Description == "" {
				return d, refuse("description is required")
			}
			if len(d.Description) > MaxDescriptionBytes {
				return d, refuse("description is %d bytes; the most is %d", len(d.Description), MaxDescriptionBytes)
			}
		case "license":
			if len(fmString(f.val)) > MaxSkillLicense {
				return d, refuse("license is over %d bytes", MaxSkillLicense)
			}
		case "compatibility":
			if len(fmString(f.val)) > MaxSkillCompatibility {
				return d, refuse("compatibility is over %d bytes", MaxSkillCompatibility)
			}
		case "metadata":
			if len(fmString(f.val)) > MaxSkillMetadata {
				return d, refuse("metadata is over %d bytes", MaxSkillMetadata)
			}
		case "allowed-tools":
			tools, err := fmList(f.key, f.val)
			if err != nil {
				return d, err
			}
			if len(tools) > MaxSkillAllowedTools {
				return d, refuse("allowed-tools has %d entries; the most is %d", len(tools), MaxSkillAllowedTools)
			}
			for _, t := range tools {
				if len(t) > MaxSkillTool {
					return d, refuse("an allowed-tools entry is over %d bytes", MaxSkillTool)
				}
			}
		case "disable-model-invocation":
			if _, err := fmBool(f.key, f.val); err != nil {
				return d, err
			}
		}
	}
	if !haveName {
		return d, refuse("name is required")
	}
	if !haveDesc {
		return d, refuse("description is required")
	}
	if strings.TrimSpace(body) == "" {
		return d, refuse("the skill has no instructions after the front matter")
	}
	d.Body = strings.TrimLeft(body, "\n")
	return d, nil
}
