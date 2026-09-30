package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// goalDecider answers the three goal options with fixed scores.
type goalDecider struct{ complete, partial, notStarted float64 }

func (d *goalDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	out := map[string]decisions.Answer{}
	for id := range r.Questions {
		v := d.notStarted
		switch id {
		case "s:complete":
			v = d.complete
		case "s:partial":
			v = d.partial
		}
		out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *goalDecider) Identity() string           { return "fake/systemone" }
func (d *goalDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

// judgeCalls records the payloads the model judge received.
type judgeCalls struct {
	mu       sync.Mutex
	payloads []rolemanager.ClassifierPayload
}

func (c *judgeCalls) pipe(reply string) *rolemanager.Pipeline {
	return &rolemanager.Pipeline{Classifier: rolemanager.ClassifierFunc(
		func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.payloads = append(c.payloads, p)
			return reply, nil
		})}
}

func goalJudgeSession(t *testing.T, dec decisions.Decider) *Session {
	t.Helper()
	s := scaleSession(t, nil)
	if dec != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(dec)}
	}
	return s
}

func TestClearlyCompleteSkipsTheModelJudge(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &goalDecider{complete: 0.97, partial: 0.02, notStarted: 0.01})
	l := passLedger{goalText: "add a flag"}
	got, stop, err := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_PARTIAL"), &l, "evidence", func(Event) {})
	if err != nil || stop || got != rolemanager.GoalComplete {
		t.Fatalf("got %q stop=%v err=%v", got, stop, err)
	}
	if len(calls.payloads) != 0 {
		t.Fatalf("the model judge ran %d times on a clear verdict", len(calls.payloads))
	}
	if l.jevUnsure {
		t.Fatal("a clear verdict must not ask for the extra verification")
	}
}

func TestClearlyNotStartedSkipsTheModelJudge(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &goalDecider{complete: 0.01, partial: 0.05, notStarted: 0.96})
	l := passLedger{goalText: "add a flag"}
	got, _, _ := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_COMPLETE"), &l, "evidence", func(Event) {})
	if got != rolemanager.GoalNotStarted || len(calls.payloads) != 0 {
		t.Fatalf("got %q with %d model calls", got, len(calls.payloads))
	}
}

func TestUnclearVerdictGoesToTheModelJudgeWithTheScores(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &goalDecider{complete: 0.6, partial: 0.5, notStarted: 0.05})
	l := passLedger{goalText: "add a flag"}
	got, _, err := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_COMPLETE"), &l, "evidence", func(Event) {})
	if err != nil || got != rolemanager.GoalComplete {
		t.Fatalf("the model judge's answer must stand: %q %v", got, err)
	}
	if len(calls.payloads) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls.payloads))
	}
	p := calls.payloads[0]
	if !strings.Contains(p.User, "Decision model scores:") || !strings.Contains(p.User, "complete: 60%") {
		t.Fatalf("the scores did not reach the model judge:\n%s", p.User)
	}
	if !strings.Contains(p.System, "checked with tools") {
		t.Fatalf("the judge was not told to demand tool-backed checks:\n%s", p.System)
	}
	if !l.jevUnsure {
		t.Fatal("an unclear verdict must arm the stronger verification wording")
	}
	l.writes = 1
	if !strings.Contains(l.gateDirective(), "verify it properly with your tools") {
		t.Fatalf("gate directive = %q", l.gateDirective())
	}
}

func TestNoBackendKeepsTheOrdinaryJudge(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, nil)
	l := passLedger{goalText: "add a flag"}
	if _, _, err := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_PARTIAL"), &l, "evidence", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(calls.payloads) != 1 || strings.Contains(calls.payloads[0].User, "Decision model scores") || strings.Contains(calls.payloads[0].System, "decision model") {
		t.Fatalf("no backend must leave the payload as it was: %+v", calls.payloads)
	}
	if l.jevUnsure {
		t.Fatal("no backend, no hint")
	}
}

type failingDecider struct{}

func (failingDecider) Decide(context.Context, decisions.Request) (decisions.Result, error) {
	return decisions.Result{}, context.DeadlineExceeded
}
func (failingDecider) Identity() string           { return "fake/systemone" }
func (failingDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func TestBackendFailureFallsBackToTheModelJudgeWithoutAHint(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, failingDecider{})
	l := passLedger{goalText: "add a flag"}
	got, stop, err := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_PARTIAL"), &l, "evidence", func(Event) {})
	if err != nil || stop || got != rolemanager.GoalPartial {
		t.Fatalf("got %q stop=%v err=%v", got, stop, err)
	}
	if len(calls.payloads) != 1 || strings.Contains(calls.payloads[0].User, "Decision model scores") || l.jevUnsure {
		t.Fatalf("a failed backend must leave the ordinary judge alone: %+v unsure=%v", calls.payloads, l.jevUnsure)
	}
}

func TestSwitchOffKeepsTheOrdinaryJudge(t *testing.T) {
	calls := &judgeCalls{}
	s := goalJudgeSession(t, &goalDecider{complete: 0.99, partial: 0, notStarted: 0})
	s.jev.On = func(job config.JevJob) bool { return job != config.JevGoalJudge }
	l := passLedger{goalText: "g"}
	got, _, _ := s.evaluateGoalPass(context.Background(), calls.pipe("GOAL_PARTIAL"), &l, "evidence", func(Event) {})
	if got != rolemanager.GoalPartial || len(calls.payloads) != 1 {
		t.Fatalf("the switch must turn the job off: %q, %d calls", got, len(calls.payloads))
	}
}
