package rolemanager

import "testing"

func TestSentinelLabelsAreDefined(t *testing.T) {
	cases := []struct {
		s    Sentinel
		want string
	}{
		{SentinelSafe, "content verified as safe"},
		{SentinelMalformed, "classifier reply was malformed"},
		{SentinelPromptInjection, "possible prompt injection detected"},
		{SentinelJailbreak, "attempted jailbreak detected"},
		{SentinelDataExtraction, "possible data extraction detected"},
		{SentinelModelExtraction, "possible model extraction detected"},
	}
	for _, c := range cases {
		if got := c.s.Label(); got != c.want {
			t.Errorf("%q.Label() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestSentinelLabelFallback(t *testing.T) {
	unknown := Sentinel("UNKNOWN")
	if got := unknown.Label(); got != string(unknown) {
		t.Errorf("unknown label fallback = %q, want %q", got, string(unknown))
	}
}

func TestPlanSentinelLabelsAreDefined(t *testing.T) {
	cases := []struct {
		s    PlanSentinel
		want string
	}{
		{PlanComplete, "plan is complete and ready to execute"},
		{PlanPartial, "plan is partial; another pass allowed"},
		{PlanNotStarted, "plan has not started"},
	}
	for _, c := range cases {
		if got := c.s.Label(); got != c.want {
			t.Errorf("%q.Label() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestPlanSentinelLabelFallback(t *testing.T) {
	unknown := PlanSentinel("UNKNOWN")
	if got := unknown.Label(); got != string(unknown) {
		t.Errorf("unknown label fallback = %q, want %q", got, string(unknown))
	}
}

func TestGoalSentinelLabelsAreDefined(t *testing.T) {
	cases := []struct {
		s    GoalSentinel
		want string
	}{
		{GoalComplete, "goal is complete"},
		{GoalPartial, "goal is partial; another pass allowed"},
		{GoalNotStarted, "goal has not started"},
	}
	for _, c := range cases {
		if got := c.s.Label(); got != c.want {
			t.Errorf("%q.Label() = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestGoalSentinelLabelFallback(t *testing.T) {
	unknown := GoalSentinel("UNKNOWN")
	if got := unknown.Label(); got != string(unknown) {
		t.Errorf("unknown label fallback = %q, want %q", got, string(unknown))
	}
}

func TestRefusalErrorUsesLabel(t *testing.T) {
	err := &RefusalError{Sentinel: SentinelJailbreak}
	want := "refusing prompt: attempted jailbreak detected"
	if got := err.Error(); got != want {
		t.Errorf("RefusalError.Error() = %q, want %q", got, want)
	}
}
