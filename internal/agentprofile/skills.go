package agentprofile

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/skills"
)

// Limits for a profile's skills list and free-form metadata.
const (
	// MaxSkills is how many skills a profile may name.
	MaxSkills = 8
	// maxSkillName bounds one name: a library skill, or "plugin:skill".
	maxSkillName = 128
)

var (
	skillNameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)
	metadataKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
)

// validateSkills checks the skills list. A named skill must be loadable, so
// when the profile narrows its tools it must keep the Skill tool.
func (p AgentProfile) validateSkills() error {
	if len(p.Skills) == 0 {
		return nil
	}
	if len(p.Skills) > MaxSkills {
		return fmt.Errorf("skills lists %d names; the most is %d", len(p.Skills), MaxSkills)
	}
	seen := map[string]bool{}
	for _, n := range p.Skills {
		if len(n) > maxSkillName || !skillNameRE.MatchString(n) {
			return fmt.Errorf("skill name %q must be lowercase letters, digits and . _ : - (at most %d bytes)", clipName(n), maxSkillName)
		}
		if seen[n] {
			return fmt.Errorf("skill %q is listed twice", n)
		}
		seen[n] = true
		if skills.ReservedName(n) && !p.Builtin && !builtinSkill(n) {
			return fmt.Errorf("skill %q is not one of Belai's own skills", n)
		}
	}
	if len(p.Tools) > 0 && !hasTool(p.Tools, "Skill") {
		return fmt.Errorf("skills needs the Skill tool in tools")
	}
	return nil
}

func builtinSkill(name string) bool {
	for _, e := range skills.Builtin() {
		if e.Name == name {
			return true
		}
	}
	return false
}

func hasTool(tools []string, name string) bool {
	for _, t := range tools {
		if t == name {
			return true
		}
	}
	return false
}

func clipName(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// validateMetadata checks the free-form metadata. It holds what an import could
// not place in a field of this schema. It is data for people and tools, never
// shown to a model, and it never changes what the agent may do.
func (p AgentProfile) validateMetadata() error {
	if len(p.Metadata) == 0 {
		return nil
	}
	if err := skills.CheckMetadataBounds(p.Metadata); err != nil {
		return err
	}
	for k, v := range p.Metadata {
		if !metadataKeyRE.MatchString(k) {
			return fmt.Errorf("metadata key %q must be letters, digits and . _ : / - (at most 64 bytes)", clipName(k))
		}
		if !cleanLine(v, skills.MaxMetadataValue) {
			return fmt.Errorf("metadata %q must be one clean line of at most %d characters", k, skills.MaxMetadataValue)
		}
	}
	return nil
}

// SkillsLine is the harness's one sentence pointing a profile's agent at its
// skills. It carries the skill names and nothing else; the skill text is read
// through the Skill tool, which classifies it. Empty when there are none.
func (p AgentProfile) SkillsLine() string {
	if len(p.Skills) == 0 {
		return ""
	}
	q := make([]string, len(p.Skills))
	for i, n := range p.Skills {
		q[i] = "`" + n + "`"
	}
	if len(q) == 1 {
		return "Your specialist guidance is the skill " + q[0] + ". Load it with the Skill tool before you start, then follow it."
	}
	return "Your specialist guidance is the skills " + strings.Join(q, ", ") + ". Load them with the Skill tool before you start, then follow them."
}
