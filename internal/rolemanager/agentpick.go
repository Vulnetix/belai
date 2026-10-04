package rolemanager

import (
	"fmt"
	"time"
)

// RecordAgentPick emits the outcome of letting Auto mode choose an agent
// profile: verdict "engaged" (one profile was a clear fit), "default" (none
// was, so the turn ran without one) or "unknown" (the backend could not
// settle it). offered is how many profiles were on offer. The profile's name
// is never recorded.
func RecordAgentPick(verdict string, offered int, model string, took time.Duration) {
	recordTimed(EventAgentPick, verdict, "", fmt.Sprintf("offered=%d", offered), 0, model, took)
}

func agentPickDescription(a Activity) Description {
	d := Description{
		Summary: "Chose which of your agent profiles fits the request",
		Outcome: "could not settle it, so the turn ran without a profile",
		Tone:    ToneNeutral,
		Levels:  LevelAll,
	}
	switch a.Verdict {
	case "engaged":
		d.Outcome = "one profile was a clear fit, so it carried the turn"
		d.Tone = ToneClear
		d.Levels = LevelDecisions
	case "default":
		d.Outcome = "no profile was a clear fit, so the turn ran without one"
	}
	return d
}
