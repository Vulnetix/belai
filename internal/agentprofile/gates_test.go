package agentprofile

import "testing"

func TestGatesSpecValidation(t *testing.T) {
	ok := worker()
	ok.Kanban.HandoffLabels = []string{"build"}
	for _, mode := range []string{"", VerifyOff, VerifyRecord, VerifyEnforce} {
		ok.Kanban.Gates = &GatesSpec{Require: true, Verify: mode}
		if err := ok.Validate(); err != nil {
			t.Fatalf("verify %q: %v", mode, err)
		}
	}
	bad := worker()
	bad.Kanban.HandoffLabels = []string{"build"}
	bad.Kanban.Gates = &GatesSpec{Verify: "always"}
	if err := bad.Validate(); err == nil {
		t.Fatal("an unknown verify mode fails validation, so it can never mean off by accident")
	}
	noHandoff := worker()
	noHandoff.Kanban.Gates = &GatesSpec{Require: true}
	if err := noHandoff.Validate(); err == nil {
		t.Fatal("require needs a handoff to attach gates to")
	}
	verifyOnly := worker()
	verifyOnly.Kanban.Gates = &GatesSpec{Verify: VerifyEnforce}
	if err := verifyOnly.Validate(); err != nil {
		t.Fatalf("a worker that only verifies needs no handoff: %v", err)
	}
}

func TestVerifyModeDefaultsOff(t *testing.T) {
	var g *GatesSpec
	if g.VerifyMode() != VerifyOff || (&GatesSpec{}).VerifyMode() != VerifyOff {
		t.Fatal("no gates block, or no mode, means off")
	}
	if (&GatesSpec{Verify: VerifyEnforce}).VerifyMode() != VerifyEnforce {
		t.Fatal("enforce is kept")
	}
}

func TestBuiltinScoutRequiresGates(t *testing.T) {
	p, err := Load("belai:scout")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kanban.Gates == nil || !p.Kanban.Gates.Require {
		t.Fatalf("the delivery scout must file every handoff with gates: %+v", p.Kanban.Gates)
	}
}
