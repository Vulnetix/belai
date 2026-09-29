package rolemanager

import (
	"strconv"
	"strings"
)

// This file is how a decision presents itself: who made it (an Actor), what
// kind of work it was (a Category), which glyph names it (an icon id) and how
// it ended (an outcome kind and a cause). All of it is derived from the event,
// the model identity and the verdict, never from model text, so the TUI, the
// session record and the website read the same facts.

// ActorKind is who reached a decision.
type ActorKind string

const (
	// ActorJev is a decision backend (TypeSafe's Jev, a self-hosted server, the
	// local decision model): it returns numbers, never text.
	ActorJev ActorKind = "jev"
	// ActorModel is a chat model acting in a role (the classifier, the fast
	// tier, the agent model).
	ActorModel ActorKind = "model"
	// ActorHarness is deterministic code: no model was asked.
	ActorHarness ActorKind = "harness"
)

// HarnessName is how a harness decision names its actor.
const HarnessName = "belai"

// Actor identifies the decider of one activity.
type Actor struct {
	Kind ActorKind
	// Model is the full provider/model identity the activity carried; empty
	// for the harness and for a chat role that named none.
	Model string
}

// Name is the short label shown on a row: the model id without its provider
// ("jev-1.13"), "belai" for the harness, and "" for a chat role that named no
// model (the caller fills in the model it knows).
func (a Actor) Name() string {
	if a.Kind == ActorHarness {
		return HarnessName
	}
	if i := strings.LastIndex(a.Model, "/"); i >= 0 {
		return a.Model[i+1:]
	}
	return a.Model
}

// harnessEvents are decided by deterministic code alone. With no model
// identity they are the harness's; a model identity always wins.
var harnessEvents = map[Event]bool{
	EventSecuritySentinelMalformed: true,
	EventVerdictCacheHit:           true,
	EventVerdictCacheBad:           true,
	EventModeForced:                true,
	EventModeGoalLengthLimit:       true,
	EventBoundarySeal:              true,
	EventBoundaryVerifyFailure:     true,
	EventToolCallMismatch:          true,
	EventAgentPoolAdmit:            true,
	EventLSPDetect:                 true,
	EventLSPDiagnose:               true,
	EventLSPServerDown:             true,
	EventRouteFallback:             true,
	EventBashSwap:                  true,
	EventOptionOrder:               true,
	EventPruneCompaction:           true,
	EventToolSearch:                true,
	EventToolSelect:                true,
	EventLSPTriage:                 true,
	EventExploreLocate:             true,
}

// IsJevIdentity reports whether a provider/model identity names a decision
// backend rather than a chat model.
func IsJevIdentity(model string) bool {
	m := strings.ToLower(model)
	for _, k := range []string{"jev", "decider", "plumb", "decision-local", "systemone"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// ActorOf says who decided a. A Jev identity is Jev, any other identity is a
// chat model, and an activity with none is the harness when its event is
// deterministic and otherwise a chat role whose model the caller knows.
func ActorOf(a Activity) Actor {
	switch {
	case a.Model != "" && IsJevIdentity(a.Model):
		return Actor{Kind: ActorJev, Model: a.Model}
	case a.Model != "":
		return Actor{Kind: ActorModel, Model: a.Model}
	case harnessEvents[a.Event]:
		return Actor{Kind: ActorHarness}
	}
	return Actor{Kind: ActorModel}
}

// Category groups activities by the kind of work, for the colour a row's icon
// takes. It is separate from Tone, which colours the outcome, so what and how
// it went never share one channel.
type Category string

const (
	CategorySecurity     Category = "security"
	CategoryMode         Category = "mode"
	CategoryContext      Category = "context"
	CategoryTools        Category = "tools"
	CategoryCode         Category = "code"
	CategoryAsk          Category = "ask"
	CategoryRouting      Category = "routing"
	CategoryHousekeeping Category = "housekeeping"
)

// Categories lists every category in display order.
var Categories = []Category{
	CategorySecurity, CategoryMode, CategoryContext, CategoryTools,
	CategoryCode, CategoryAsk, CategoryRouting, CategoryHousekeeping,
}

// presentation is one event's category and icon id.
type presentation struct {
	Category Category
	Icon     string
}

var presentations = map[Event]presentation{
	EventSecuritySentinel:          {CategorySecurity, "shield"},
	EventSecuritySentinelMalformed: {CategorySecurity, "shield_unclear"},
	EventSecurityPhase:             {CategorySecurity, "phase"},
	EventSecurityFallback:          {CategorySecurity, "shield_fallback"},
	EventVerdictCacheHit:           {CategorySecurity, "recall"},
	EventVerdictCacheBad:           {CategorySecurity, "recall_bad"},
	EventBoundarySeal:              {CategorySecurity, "seal"},
	EventBoundaryVerifyFailure:     {CategorySecurity, "seal_broken"},
	EventToolCallMismatch:          {CategorySecurity, "mismatch"},
	EventModeClassify:              {CategoryMode, "mode"},
	EventModeDetect:                {CategoryMode, "intent"},
	EventModeForced:                {CategoryMode, "pin"},
	EventModeGoalLengthLimit:       {CategoryMode, "limit"},
	EventGoalEval:                  {CategoryMode, "goal"},
	EventGoalEvalRepair:            {CategoryMode, "goal_repair"},
	EventPlanEval:                  {CategoryMode, "plan"},
	EventAgentEval:                 {CategoryMode, "agent"},
	EventGoalDraft:                 {CategoryMode, "draft"},
	EventCompactionSummary:         {CategoryContext, "compact"},
	EventPruneCompaction:           {CategoryContext, "prune"},
	EventWebFetchAnswer:            {CategoryContext, "page"},
	EventDepChange:                 {CategoryContext, "deps"},
	EventBashSwap:                  {CategoryTools, "swap"},
	EventBashReplan:                {CategoryTools, "replan"},
	EventToolSearch:                {CategoryTools, "search"},
	EventToolSelect:                {CategoryTools, "select"},
	EventLSPDetect:                 {CategoryCode, "server"},
	EventLSPDiagnose:               {CategoryCode, "diagnose"},
	EventLSPServerDown:             {CategoryCode, "server_down"},
	EventLSPTriage:                 {CategoryCode, "triage"},
	EventExploreLocate:             {CategoryCode, "locate"},
	EventClarify:                   {CategoryAsk, "clarify"},
	EventOptionOrder:               {CategoryAsk, "options"},
	EventRouteFallback:             {CategoryRouting, "route"},
	EventAgentPoolAdmit:            {CategoryRouting, "pool"},
	EventSessionName:               {CategoryHousekeeping, "name"},
}

// CategoryOf is the category of an event; housekeeping for an unknown one.
func CategoryOf(e Event) Category {
	if p, ok := presentations[e]; ok {
		return p.Category
	}
	return CategoryHousekeeping
}

// IconOf is the stable icon id of an event. The glyph is the TUI's choice, so
// a theme can change it without rewriting a session file.
func IconOf(e Event) string {
	if p, ok := presentations[e]; ok {
		return p.Icon
	}
	return "dot"
}

// Icons lists every icon id the events use, in no particular order.
func Icons() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range presentations {
		if !seen[p.Icon] {
			seen[p.Icon] = true
			out = append(out, p.Icon)
		}
	}
	return out
}

// OutcomeKind is the coarse result of a decision, the persisted form of Tone.
type OutcomeKind string

const (
	OutcomeClear   OutcomeKind = "clear"
	OutcomeCaution OutcomeKind = "caution"
	OutcomeBlocked OutcomeKind = "blocked"
	OutcomeNeutral OutcomeKind = "neutral"
)

// Kind maps a Tone to its OutcomeKind.
func (t Tone) Kind() OutcomeKind {
	switch t {
	case ToneClear:
		return OutcomeClear
	case ToneCaution:
		return OutcomeCaution
	case ToneBlocked:
		return OutcomeBlocked
	}
	return OutcomeNeutral
}

// Cause is why a decision came out the way it did, when that is not the plain
// path: it fell back, came from a cache, or ran out of time.
type Cause string

const (
	CauseNone     Cause = "none"
	CauseFallback Cause = "fallback"
	CauseCacheHit Cause = "cache_hit"
	CauseTimeout  Cause = "timeout"
)

// CauseOf derives the cause from the event and verdict.
func CauseOf(a Activity) Cause {
	switch a.Event {
	case EventVerdictCacheHit:
		return CauseCacheHit
	case EventSecurityFallback, EventRouteFallback:
		return CauseFallback
	}
	switch strings.ToLower(a.Verdict) {
	case "fallback":
		return CauseFallback
	case "timeout", "timed_out":
		return CauseTimeout
	}
	return CauseNone
}

// ScorePct reads the decision score out of a harness-authored "score=N"
// token in Detail, or -1. Only digits are accepted, so the value is a number
// and never text.
func ScorePct(detail string) int {
	for _, f := range strings.Fields(detail) {
		if v, ok := strings.CutPrefix(f, "score="); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= -1 && n <= 100 {
				return n
			}
		}
	}
	return -1
}
