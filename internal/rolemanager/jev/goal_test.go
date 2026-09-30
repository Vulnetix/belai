package jev

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func TestPickGoal(t *testing.T) {
	cases := []struct {
		name   string
		scores map[string]float64
		want   GoalVerdict
	}{
		{"clearly complete", map[string]float64{"complete": 0.96, "partial": 0.05, "not_started": 0.01}, GoalComplete},
		{"at the cut-off", map[string]float64{"complete": 0.90, "partial": 0.20, "not_started": 0.20}, GoalComplete},
		{"just under", map[string]float64{"complete": 0.89, "partial": 0.05, "not_started": 0.01}, GoalUnclear},
		{"a live rival is not clear", map[string]float64{"complete": 0.95, "partial": 0.40, "not_started": 0.01}, GoalUnclear},
		{"clearly not started", map[string]float64{"complete": 0.02, "partial": 0.1, "not_started": 0.95}, GoalNotStarted},
		{"partial leads", map[string]float64{"complete": 0.3, "partial": 0.8, "not_started": 0.05}, GoalPartial},
		{"contested", map[string]float64{"complete": 0.5, "partial": 0.5, "not_started": 0.05}, GoalUnclear},
		{"an unanswered option is unknown, not low", map[string]float64{"complete": 0.99, "partial": 0.01}, GoalUnclear},
		{"none", nil, GoalUnclear},
	}
	for _, c := range cases {
		if got, _ := PickGoal(c.scores); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestPickGoalReportsPercentagesAndMissing(t *testing.T) {
	_, s := PickGoal(map[string]float64{"complete": 0.734, "partial": 0.2})
	if s.Complete != 73 || s.Partial != 20 || s.NotStarted != -1 {
		t.Fatalf("scores = %+v", s)
	}
}

func TestPickGoalFollowsTheSettings(t *testing.T) {
	th := config.DefaultJevThresholds()
	th.GoalCompleteAt = 0.99
	withThresholds(t, th)
	if got, _ := PickGoal(map[string]float64{"complete": 0.95, "partial": 0.05, "not_started": 0.01}); got != GoalUnclear {
		t.Fatalf("goal_complete_at 0.99 must refuse 0.95, got %q", got)
	}
}

func TestRateGoalAsksOneQuestionPerOption(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for id := range r.Questions {
			v := 0.03
			if id == "s:complete" {
				v = 0.97
			}
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
		}
		return out, nil
	}}
	j := &Jobs{Client: NewWith(d)}
	if !j.Enabled(config.JevGoalJudge) {
		t.Fatal("the job is not enabled with a backend")
	}
	scores, _, err := j.RateGoal(context.Background(), GoalFacts{Goal: "add a flag", Todos: "[x] add flag", Facts: "Pass: 1"})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := PickGoal(scores); v != GoalComplete {
		t.Fatalf("scores %v gave %q", scores, v)
	}
}

func TestGoalJudgeJobIsASwitchDefaultingOn(t *testing.T) {
	j := &Jobs{Client: NewWith(&fakeDecider{}), On: func(job config.JevJob) bool { return job != config.JevGoalJudge }}
	if j.Enabled(config.JevGoalJudge) {
		t.Fatal("the switch did not turn the job off")
	}
}
