// Package rolemanager owns the role-manager activity vocabulary: the typed
// events, the plain-English descriptions the TUI renders, and the observer
// hook record() fans out to. It sits beside labels.go for the same reason the
// sentinel labels do: a token and its human wording stay in one package with
// one parity test.
package rolemanager

import (
	"strings"
	"sync"
	"sync/atomic"
)

// Event names one role-manager decision, typed so a new event cannot be added
// without a const, and the parity test can assert every const has a
// description (or is deliberately suppressed).
type Event string

const (
	EventSecuritySentinel          Event = "security_sentinel"
	EventSecuritySentinelMalformed Event = "security_sentinel_malformed"
	EventSecurityPhase             Event = "security_phase"
	EventVerdictCacheHit           Event = "verdict_cache_hit"
	EventVerdictCacheBad           Event = "verdict_cache_bad"
	EventModeClassify              Event = "mode_classify"
	EventModeForced                Event = "mode_forced"
	EventModeGoalLengthLimit       Event = "mode_goal_length_limit"
	EventGoalEval                  Event = "goal_eval"
	EventGoalEvalRepair            Event = "goal_eval_repair"
	EventPlanEval                  Event = "plan_eval"
	EventAgentEval                 Event = "agent_eval"
	EventGoalDraft                 Event = "goal_draft"
	EventClarify                   Event = "clarify"
	EventCompactionSummary         Event = "compaction_summary"
	EventSessionName               Event = "session_name"
	EventBoundarySeal              Event = "boundary_seal"
	EventBoundaryVerifyFailure     Event = "boundary_verify_failure"
	EventToolCallMismatch          Event = "tool_call_mismatch"
	EventAgentPoolAdmit            Event = "agent_pool_admit"
	EventLSPDetect                 Event = "lsp_detect"
	EventLSPDiagnose               Event = "lsp_diagnose"
	EventLSPServerDown             Event = "lsp_server_down"
)

// Level is the display granularity of the internal-work feed. Order matters:
// the TUI's gate keeps a message when its level is <= the resolved setting, so
// higher values are strictly more verbose (hidden < decisions < security < all).
type Level int

const (
	LevelHidden Level = iota
	LevelDecisions
	LevelSecurity
	LevelAll
)

// Tone colours an activity line's outcome word.
type Tone int

const (
	ToneNeutral Tone = iota
	ToneClear
	ToneCaution
	ToneBlocked
)

// Activity is one recorded decision. Its fields are exactly trace.Record's
// bounded metadata — never classified payload text, never a credential.
type Activity struct {
	Event   Event
	Verdict string
	Subject string // the trace Tool field
	Detail  string
	Pass    int
	// Model carries the provider/model identity that produced the verdict
	// ("openrouter/typesafe/jev-1.13", "embedded/GuardrailsAI/…"). Empty when
	// the activity did not invoke a model.
	Model string
}

// Description is the plain-English rendering of one Activity. It never carries
// the Activity's Detail: only harness-authored structure parsed from Detail
// reaches the screen.
type Description struct {
	Summary string
	Outcome string
	Tone    Tone
	Levels  Level
}

// suppressedEvents are already surfaced by dedicated TUI lines, so the feed
// must not print them a second time. One named set, one obvious place to
// revisit if a dedicated line is ever removed.
var suppressedEvents = map[Event]bool{
	EventModeClassify:   true,
	EventModeForced:     true,
	EventGoalEval:       true,
	EventGoalEvalRepair: true,
	EventPlanEval:       true,
}

// Observer receives every recorded activity. It is called from hot classifier
// paths on several goroutines, so implementations must be non-blocking (the
// TUI does a non-blocking send and drops on overflow). Dropping is correct:
// the feed is render-only.
type Observer func(Activity)

// observer is the single registered sink, guarded by an atomic pointer exactly
// like traceWriter().
var observer atomic.Pointer[Observer]

// SetObserver registers the in-process activity observer and returns a cancel
// that detaches it. SetObserver(nil) detaches immediately. The slot is global,
// so registering a second observer replaces the first; the returned cancel is
// idempotent.
func SetObserver(fn Observer) (cancel func()) {
	if fn == nil {
		observer.Store(nil)
		return func() {}
	}
	observer.Store(&fn)
	var once sync.Once
	return func() {
		once.Do(func() {
			if observer.Load() == &fn {
				observer.Store(nil)
			}
		})
	}
}

// ParseLevel maps a stored level name to its Level. Unrecognised names read as
// hidden (fail closed on display).
func ParseLevel(name string) Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "decisions":
		return LevelDecisions
	case "security":
		return LevelSecurity
	case "all":
		return LevelAll
	default:
		return LevelHidden
	}
}

// Describe renders an Activity into its plain-English Description. It returns
// false for the suppressed set, so callers drop those before rendering.
func Describe(a Activity) (Description, bool) {
	if suppressedEvents[a.Event] {
		return Description{}, false
	}
	switch a.Event {
	case EventSecuritySentinel:
		return securityDescription(a), true
	case EventSecurityPhase:
		return phaseDescription(a), true
	case EventSecuritySentinelMalformed:
		return Description{
			Summary: "Checked what " + subjectPhrase(a.Subject) + " returned",
			Outcome: "couldn't tell — held it back to be safe",
			Tone:    ToneCaution,
			Levels:  LevelSecurity,
		}, true
	case EventVerdictCacheHit:
		return Description{
			Summary: "Recognised " + subjectPhrase(a.Subject) + " from earlier",
			Outcome: "reused the earlier result",
			Tone:    ToneNeutral,
			Levels:  LevelAll,
		}, true
	case EventVerdictCacheBad:
		return Description{
			Summary: "Recognised " + subjectPhrase(a.Subject) + " as something refused before",
			Outcome: "blocked again, no recheck",
			Tone:    ToneBlocked,
			Levels:  LevelSecurity,
		}, true
	case EventBoundarySeal:
		return Description{
			Summary: "Sealed " + countOr("blocks", a.Detail, "the") + " instruction blocks before sending",
			Outcome: "done",
			Tone:    ToneNeutral,
			Levels:  LevelAll,
		}, true
	case EventBoundaryVerifyFailure:
		return Description{
			Summary: "Refused to put outside text into Signet's own instructions",
			Outcome: "blocked",
			Tone:    ToneBlocked,
			Levels:  LevelSecurity,
		}, true
	case EventToolCallMismatch:
		return Description{
			Summary: strings.TrimSpace(a.Subject) + " was asked for but never offered this turn",
			Outcome: "stopped the turn",
			Tone:    ToneBlocked,
			Levels:  LevelDecisions,
		}, true
	case EventModeGoalLengthLimit:
		return Description{
			Summary: "Request too long to track as a single goal",
			Outcome: "handled it as ordinary work",
			Tone:    ToneCaution,
			Levels:  LevelDecisions,
		}, true
	case EventAgentPoolAdmit:
		kind := field(a.Detail, "kind")
		if kind == "" {
			kind = "helper"
		}
		slot := field(a.Detail, "slot")
		if slot == "" {
			slot = "?"
		}
		return Description{
			Summary: "Queued a " + kind + " helper (slot " + slot + ")",
			Outcome: "running / waiting for a slot",
			Tone:    ToneNeutral,
			Levels:  LevelAll,
		}, true
	case EventAgentEval:
		return agentEvalDescription(a), true
	case EventGoalDraft:
		return goalDraftDescription(a), true
	case EventClarify:
		return clarifyDescription(a), true
	case EventCompactionSummary:
		return compactionDescription(a), true
	case EventSessionName:
		return sessionNameDescription(a), true
	case EventLSPDiagnose:
		return lspDiagnoseDescription(a), true
	case EventLSPDetect:
		return lspDetectDescription(a), true
	case EventLSPServerDown:
		return lspServerDownDescription(a), true
	}
	return Description{}, false
}

func securityDescription(a Activity) Description {
	d := Description{
		Summary: "Checked what " + subjectPhrase(a.Subject) + " returned for hidden instructions",
		Outcome: "clean",
		Tone:    ToneClear,
		Levels:  LevelSecurity,
	}
	switch a.Verdict {
	case string(SentinelPromptInjection):
		d.Outcome = "blocked — reads like an attempt to hijack the instructions"
		d.Tone = ToneBlocked
	case string(SentinelJailbreak):
		d.Outcome = "blocked — reads like an attempt to override the rules"
		d.Tone = ToneBlocked
	case string(SentinelDataExtraction):
		d.Outcome = "blocked — reads like an attempt to pull out training data"
		d.Tone = ToneBlocked
	case string(SentinelModelExtraction):
		d.Outcome = "blocked — reads like an attempt to copy the model"
		d.Tone = ToneBlocked
	}
	return d
}

// phaseDescription renders one ML classifier stage's verdict. The subject is
// the stage key ("phase 1" / "phase 2" / "phase 3"); the displayed text
// explains why that stage is run, not what it is called. The verdict is a
// sentinel token, or a status word ("skipped" / "off") for a stage that did
// not run.
func phaseDescription(a Activity) Description {
	summary := "Checked whether the content floods the prompt with repeated instructions"
	switch a.Subject {
	case "phase 2":
		summary = "Checked whether the content tries to override the rules"
	case "phase 3":
		summary = "Checked whether the content tries to extract private data or model details"
	}
	d := Description{
		Summary: summary,
		Levels:  LevelSecurity,
	}
	switch a.Verdict {
	case string(SentinelSafe):
		d.Outcome = "clean"
		d.Tone = ToneClear
	case string(SentinelPromptInjection):
		d.Outcome = "blocked — reads like an attempt to hijack the instructions"
		d.Tone = ToneBlocked
	case string(SentinelJailbreak):
		d.Outcome = "blocked — reads like an attempt to override the rules"
		d.Tone = ToneBlocked
	case string(SentinelDataExtraction):
		d.Outcome = "blocked — reads like an attempt to pull out private data"
		d.Tone = ToneBlocked
	case string(SentinelModelExtraction):
		d.Outcome = "blocked — reads like an attempt to copy the model"
		d.Tone = ToneBlocked
	case "skipped":
		d.Outcome = "skipped — an earlier check already flagged it"
		d.Tone = ToneNeutral
	case "off":
		d.Outcome = "off"
		d.Tone = ToneNeutral
	default:
		d.Outcome = "couldn't tell"
		d.Tone = ToneCaution
	}
	return d
}

func agentEvalDescription(a Activity) Description {
	d := Description{
		Summary: "Reviewed the background agent's last run",
		Levels:  LevelDecisions,
	}
	switch a.Verdict {
	case string(AgentContinue):
		d.Outcome = "let it keep going"
		d.Tone = ToneClear
	case string(AgentPause):
		d.Outcome = "paused it for you"
		d.Tone = ToneCaution
	case string(AgentSleep):
		d.Outcome = "put it to sleep"
		d.Tone = ToneNeutral
	default:
		d.Outcome = "stopped it"
		d.Tone = ToneCaution
	}
	return d
}

func goalDraftDescription(a Activity) Description {
	switch a.Verdict {
	case "usable":
		return Description{
			Summary: "Wrote the completion checklist for the goal",
			Outcome: "ready",
			Tone:    ToneClear,
			Levels:  LevelDecisions,
		}
	default: // "empty", "missing_objective"
		return Description{
			Summary: "Tried to write the completion checklist",
			Outcome: "couldn't — using your words as written",
			Tone:    ToneCaution,
			Levels:  LevelDecisions,
		}
	}
}

func clarifyDescription(a Activity) Description {
	switch a.Verdict {
	case "usable":
		return Description{
			Summary: "Drafted " + countOr("groups", a.Detail, "?") + " questions to pin down the request",
			Outcome: "ready to ask",
			Tone:    ToneClear,
			Levels:  LevelDecisions,
		}
	default: // "invalid"
		return Description{
			Summary: "Tried to draft clarifying questions (attempt " + countOr("attempt", a.Detail, "?") + ")",
			Outcome: "unusable — carrying on without them",
			Tone:    ToneCaution,
			Levels:  LevelDecisions,
		}
	}
}

func compactionDescription(a Activity) Description {
	if a.Verdict == "valid" {
		return Description{
			Summary: "Summarised the conversation to free up room",
			Outcome: "done",
			Tone:    ToneClear,
			Levels:  LevelDecisions,
		}
	}
	return Description{
		Summary: "Tried to summarise the conversation",
		Outcome: "incomplete — kept the session as it was",
		Tone:    ToneCaution,
		Levels:  LevelDecisions,
	}
}

func sessionNameDescription(a Activity) Description {
	if a.Verdict == "valid" {
		return Description{
			Summary: "Named this session from your first message",
			Outcome: "done",
			Tone:    ToneNeutral,
			Levels:  LevelDecisions,
		}
	}
	return Description{
		Summary: "Tried to name this session",
		Outcome: "reply was unusable — left it unnamed",
		Tone:    ToneNeutral,
		Levels:  LevelDecisions,
	}
}

// lspDiagnoseDescription renders one language-server diagnostics check.
func lspDiagnoseDescription(a Activity) Description {
	lang := langPhrase(a.Subject)
	d := Description{
		Summary: "Checked the " + lang + " file Signet just edited",
		Levels:  LevelAll,
	}
	switch a.Verdict {
	case "problems":
		d.Outcome = countOr("count", a.Detail, "some") + " problems reported"
		d.Tone = ToneCaution
	case "clean":
		d.Outcome = "no problems"
		d.Tone = ToneClear
	case "warming":
		d.Outcome = "language server still starting — skipped"
		d.Tone = ToneNeutral
	case "unavailable":
		d.Outcome = "no checker available"
		d.Tone = ToneNeutral
	case "timeout":
		d.Outcome = "took too long — skipped"
		d.Tone = ToneNeutral
	default:
		d.Outcome = "couldn't tell"
		d.Tone = ToneCaution
	}
	return d
}

func lspDetectDescription(a Activity) Description {
	lang := langPhrase(a.Subject)
	server := field(a.Detail, "server")
	outcome := "none installed"
	if server != "" {
		outcome = "found " + server
	}
	return Description{
		Summary: "Looked for a " + lang + " language server",
		Outcome: outcome,
		Tone:    ToneNeutral,
		Levels:  LevelAll,
	}
}

func lspServerDownDescription(a Activity) Description {
	lang := langPhrase(a.Subject)
	return Description{
		Summary: "The " + lang + " language server stopped responding",
		Outcome: "fell back to a syntax check",
		Tone:    ToneCaution,
		Levels:  LevelDecisions,
	}
}

// subjectPhrase maps a classifier subject kind to plain English. Unknown
// subjects fall back to "that content".
func subjectPhrase(subject string) string {
	switch strings.ToLower(strings.TrimSpace(subject)) {
	case "bash":
		return "the shell command"
	case "read":
		return "the file Signet read"
	case "web_fetch", "web_search":
		return "the page Signet fetched"
	case "remote":
		return "the remote repository text"
	case "agent_store":
		return "another agent's notes"
	case "prompt":
		return "your message"
	case "steering":
		return "your steering message"
	default:
		return "that content"
	}
}

// field returns the value of a "key=value" token inside a harness-authored
// detail string (e.g. "blocks=4", "kind=explore id=x slot=1").
func field(detail, key string) string {
	for _, part := range strings.Fields(detail) {
		if k, v, ok := strings.Cut(part, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// countOr returns the numeric token for key from detail, or fallback when the
// key is absent.
func countOr(key, detail, fallback string) string {
	if v := field(detail, key); v != "" {
		return v
	}
	return fallback
}
