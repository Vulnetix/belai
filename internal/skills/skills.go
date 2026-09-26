package skills

import (
	"fmt"
	"os"

	"github.com/vulnetix/belai/internal/posture"
)

// ValidateWithPosture runs ValidateSkill and applies the skill_invalid posture.
// Under enforce (default) a validation error is returned. Under warn the
// error is printed to stderr and nil is returned. Under ignore validation is
// skipped entirely.
func ValidateWithPosture(doc string, pol posture.Policy) (*Manifest, error) {
	if pol.Level(posture.SkillInvalid) == posture.Ignore {
		return &Manifest{}, nil
	}
	m, err := ValidateSkill(doc)
	if err != nil {
		if pol.Level(posture.SkillInvalid) == posture.Warn {
			fmt.Fprintf(os.Stderr, "belai: warning: invalid skill: %v\n", err)
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}
