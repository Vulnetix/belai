package jev

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func TestPickScale(t *testing.T) {
	cases := []struct {
		name   string
		scores map[string]float64
		want   Scale
	}{
		{"clearly simple", map[string]float64{"simple": 0.95, "staged": 0.05}, ScaleSimple},
		{"at the cut-off", map[string]float64{"simple": 0.80, "staged": 0.49}, ScaleSimple},
		{"just under", map[string]float64{"simple": 0.79, "staged": 0.10}, ScaleUnknown},
		{"both high is not simple", map[string]float64{"simple": 0.9, "staged": 0.6}, ScaleUnknown},
		{"clearly staged", map[string]float64{"simple": 0.1, "staged": 0.9}, ScaleStaged},
		{"staged unanswered is unknown, not low", map[string]float64{"simple": 0.99}, ScaleUnknown},
		{"simple unanswered", map[string]float64{"staged": 0.01}, ScaleUnknown},
		{"none", nil, ScaleUnknown},
	}
	for _, c := range cases {
		if got, _ := PickScale(c.scores); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestPickScaleFollowsTheSetting(t *testing.T) {
	th := config.DefaultJevThresholds()
	th.SimpleAt = 0.95
	withThresholds(t, th)
	if got, _ := PickScale(map[string]float64{"simple": 0.9, "staged": 0.05}); got != ScaleUnknown {
		t.Fatalf("simple_at 0.95 must refuse 0.9, got %q", got)
	}
}

func TestRateScaleAsksOneQuestionPerItem(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for id := range r.Questions {
			v := 0.05
			if id == "s:simple" {
				v = 0.97
			}
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
		}
		return out, nil
	}}
	j := &Jobs{Client: NewWith(d)}
	if !j.Enabled(config.JevRequestScale) {
		t.Fatal("the job is not enabled with a backend")
	}
	scores, _, err := j.RateScale(context.Background(), "commit everything and push")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := PickScale(scores); v != ScaleSimple {
		t.Fatalf("scores %v gave %q", scores, v)
	}
}

func TestRequestScaleJobIsASwitchDefaultingOn(t *testing.T) {
	j := &Jobs{Client: NewWith(&fakeDecider{}), On: func(job config.JevJob) bool { return job != config.JevRequestScale }}
	if j.Enabled(config.JevRequestScale) {
		t.Fatal("the switch did not turn the job off")
	}
}
