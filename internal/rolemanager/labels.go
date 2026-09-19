package rolemanager

// SentinelLabels maps each security sentinel to a concise, human-readable
// statement used in the TUI. Keep this map in sync with every Sentinel
// constant; an unknown sentinel falls back to its raw token.
var SentinelLabels = map[Sentinel]string{
	SentinelSafe:            "content verified as safe",
	SentinelMalformed:       "classifier reply was malformed",
	SentinelPromptInjection: "possible prompt injection detected",
	SentinelJailbreak:       "attempted jailbreak detected",
	SentinelDataExtraction:  "possible data extraction detected",
	SentinelModelExtraction: "possible model extraction detected",
}

// Label returns the human-readable statement for a security sentinel.
// Unknown sentinels return their raw token.
func (s Sentinel) Label() string {
	if l, ok := SentinelLabels[s]; ok {
		return l
	}
	return string(s)
}

// PlanSentinelLabels maps each plan-evaluator sentinel to a concise,
// human-readable statement. Maintain parity with the PlanSentinel constants.
var PlanSentinelLabels = map[PlanSentinel]string{
	PlanComplete:   "plan is complete and ready to execute",
	PlanPartial:    "plan is partial; another pass allowed",
	PlanNotStarted: "plan has not started",
}

// Label returns the human-readable statement for a plan sentinel.
// Unknown sentinels return their raw token.
func (s PlanSentinel) Label() string {
	if l, ok := PlanSentinelLabels[s]; ok {
		return l
	}
	return string(s)
}

// GoalSentinelLabels maps each goal-evaluator sentinel to a concise,
// human-readable statement. Maintain parity with the GoalSentinel constants.
var GoalSentinelLabels = map[GoalSentinel]string{
	GoalComplete:   "goal is complete",
	GoalPartial:    "goal is partial; another pass allowed",
	GoalNotStarted: "goal has not started",
}

// Label returns the human-readable statement for a goal sentinel.
// Unknown sentinels return their raw token.
func (s GoalSentinel) Label() string {
	if l, ok := GoalSentinelLabels[s]; ok {
		return l
	}
	return string(s)
}
