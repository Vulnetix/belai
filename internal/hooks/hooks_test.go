package hooks

import (
	"testing"

	"github.com/vulnetix/belai/internal/posture"
)

func TestValidateWithPostureIgnore(t *testing.T) {
	_, err := ValidateWithPosture(Hook{Name: "x", Event: "pre_tool", Command: "ls"}, posture.Policy{posture.HookInvalid: posture.Ignore})
	if err != nil {
		t.Fatalf("expected no error under ignore")
	}
}

func TestValidateWithPostureEnforceRejects(t *testing.T) {
	_, err := ValidateWithPosture(Hook{Name: "", Event: "bad", Command: ".."}, posture.Defaults())
	if err == nil {
		t.Fatal("expected error under enforce")
	}
}

func TestValidateWithPostureWarnSwallows(t *testing.T) {
	_, err := ValidateWithPosture(Hook{Name: "", Event: "bad", Command: ".."}, posture.Policy{posture.HookInvalid: posture.Warn})
	if err != nil {
		t.Fatalf("expected no error under warn")
	}
}
