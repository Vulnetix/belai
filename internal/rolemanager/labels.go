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

// DepSentinelLabels maps each dependency-change sentinel to a concise,
// human-readable statement. Maintain parity with the DepSentinel constants.
var DepSentinelLabels = map[DepSentinel]string{
	DepsChanged:   "dependencies were added or updated",
	DepsUnchanged: "no dependency changed",
}

// Label returns the human-readable statement for a dependency-change
// sentinel. Unknown sentinels return their raw token.
func (s DepSentinel) Label() string {
	if l, ok := DepSentinelLabels[s]; ok {
		return l
	}
	return string(s)
}

// IntentLabels maps each detected intent to a concise, human-readable label
// shown in the TUI mode chip and the mode-choice panel. Maintain parity
// with the Intent constants.
var IntentLabels = map[Intent]string{
	IntentAgent:   "agent",
	IntentPlan:    "plan",
	IntentGoal:    "goal",
	IntentHandoff: "handoff",
	IntentDebug:   "debug",
	IntentFanOut:  "fan-out",
}

// Label returns the human-readable label for an intent. Unknown intents fall
// back to their raw token.
func (i Intent) Label() string {
	if l, ok := IntentLabels[i]; ok {
		return l
	}
	return string(i)
}
