package jev

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func TestPickVoiceNeedsExactlyOneTargetAtTheCutoff(t *testing.T) {
	cases := []struct {
		name   string
		scores map[string]float64
		want   string
	}{
		{"one clear hit", map[string]float64{"a": 0.97, "b": 0.3}, "a"},
		{"just under", map[string]float64{"a": 0.94, "b": 0.3}, ""},
		{"at the cut-off", map[string]float64{"a": 0.95}, "a"},
		{"two hits is ambiguous", map[string]float64{"a": 0.97, "b": 0.96}, ""},
		{"none", map[string]float64{}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got, _ := PickVoice(c.scores); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestPickVoiceFollowsTheSetting(t *testing.T) {
	th := config.DefaultJevThresholds()
	th.VoiceAt = 0.8
	withThresholds(t, th)
	if got, _ := PickVoice(map[string]float64{"a": 0.85}); got != "a" {
		t.Fatalf("voice_at 0.8 must accept 0.85, got %q", got)
	}
}

func TestVoiceAtDefaultIsNinetyFivePercent(t *testing.T) {
	if config.DefaultJevThresholds().VoiceAt != 0.95 {
		t.Fatal("a spoken instruction needs at least 95% by default")
	}
}

func TestRateVoiceSendsDecisionTextAndScoresEachTarget(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for id := range r.Questions {
			v := 0.1
			if id == "s:review" {
				v = 0.98
			}
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
		}
		return out, nil
	}}
	j := &Jobs{Client: NewWith(d)}
	if !j.Enabled(config.JevVoiceCommand) {
		t.Fatal("the job is not enabled with a backend")
	}
	scores, _, err := j.RateVoice(context.Background(), "run the security review", []VoiceTarget{
		{ID: "review", Kind: "review", Name: "security review"},
		{ID: "plan", Kind: "mode", Name: "plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := PickVoice(scores); id != "review" {
		t.Fatalf("picked %q from %v", id, scores)
	}
}

func TestVoiceJobIsASwitchDefaultingOn(t *testing.T) {
	j := &Jobs{Client: NewWith(&fakeDecider{}), On: func(job config.JevJob) bool { return job != config.JevVoiceCommand }}
	if j.Enabled(config.JevVoiceCommand) {
		t.Fatal("the switch did not turn the job off")
	}
}
