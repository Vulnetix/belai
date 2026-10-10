package agent

import (
	"strings"
	"testing"
)

// A decision turn (a reviewer, a verifier, a read-only scout) delivers a recorded
// decision, so the loop never tells it to edit, never reads a run without file
// writes as a stall, and does not spend a read-only verification pass on an
// approval that wrote nothing.
func TestDecisionLedgerNeverAsksForAnEdit(t *testing.T) {
	l := passLedger{goalText: "review it", decision: true}
	for i := 0; i < goalNoWritePasses+2; i++ {
		l.noteWrites(passOutcome{})
	}
	if l.stalledOnWrites() {
		t.Fatal("a decision turn changes no file by design, so it never stalls on writes")
	}
	bodies := map[string]string{
		"noWrite":     l.noWriteDirective(),
		"gate":        l.gateDirective(),
		"partial":     l.partialDirective(),
		"progression": l.progressionDirective(),
	}
	body, _ := l.partialDirectiveTurn()
	bodies["partialTurn"] = body
	for name, d := range bodies {
		low := strings.ToLower(d)
		if strings.Contains(low, "an edit") || strings.Contains(low, "smallest correct edit") || strings.Contains(low, "make the change") {
			t.Errorf("%s asks a decision turn to edit:\n%s", name, d)
		}
	}
	if l.gateDirective() != verificationDirective {
		t.Errorf("an approval that wrote nothing is not sent back to edit:\n%s", l.gateDirective())
	}
}

// A decision turn whose tool results were withheld is still repaired: that
// failure is the tool surface, not a worker that stopped deciding.
func TestDecisionLedgerStillRepairsWithheldResults(t *testing.T) {
	l := passLedger{goalText: "review it", decision: true, passWithheld: 2}
	if l.gateDirective() != withheldRepairDirective {
		t.Fatalf("withheld results are repaired on a decision turn too:\n%s", l.gateDirective())
	}
}

// An ordinary goal keeps every edit-oriented directive.
func TestOrdinaryLedgerKeepsTheEditDirectives(t *testing.T) {
	l := passLedger{goalText: "edit"}
	if !strings.Contains(l.noWriteDirective(), "smallest correct edit") {
		t.Errorf("noWriteDirective changed for an ordinary goal:\n%s", l.noWriteDirective())
	}
	if !strings.Contains(l.partialDirective(), "edit") || !strings.Contains(l.progressionDirective(), "edit") {
		t.Error("the partial and progression directives changed for an ordinary goal")
	}
}

func TestDecisionTurnGetsTheDecisionAckDirective(t *testing.T) {
	s := &Session{}
	s.turnDecision = true
	if got := s.goalAckDirective(); got != decisionAckDirective || strings.Contains(got, "Todo") {
		t.Fatalf("a decision turn gets the decision directive, not the edit one:\n%s", got)
	}
	s.turnDecision = false
	if s.goalAckDirective() != goalAckDirective {
		t.Fatal("an ordinary turn keeps the goal directive")
	}
	s.turnDecision, s.turnExecutePlan = true, true
	if strings.Contains(s.goalAckDirective(), "do not edit the files under review") {
		t.Fatal("an approved plan is executed, never treated as a decision turn")
	}
}
