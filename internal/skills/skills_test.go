package skills

import (
	"testing"

	"github.com/vulnetix/belai/internal/posture"
)

func TestValidateWithPostureIgnore(t *testing.T) {
	_, err := ValidateWithPosture("bad", posture.Policy{posture.SkillInvalid: posture.Ignore})
	if err != nil {
		t.Fatalf("expected no error under ignore")
	}
}

func TestValidateWithPostureEnforceRejects(t *testing.T) {
	_, err := ValidateWithPosture("bad", posture.Defaults())
	if err == nil {
		t.Fatal("expected error under enforce")
	}
}

func TestValidateWithPostureWarnSwallows(t *testing.T) {
	_, err := ValidateWithPosture("bad", posture.Policy{posture.SkillInvalid: posture.Warn})
	if err != nil {
		t.Fatalf("expected no error under warn")
	}
}
