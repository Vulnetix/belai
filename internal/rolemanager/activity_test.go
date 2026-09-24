package rolemanager

import (
	"strings"
	"testing"
)

// allEvents is the canonical list of event constants. Adding an Event without
// adding it here (and to Describe or suppressedEvents) fails the parity test.
var allEvents = []Event{
	EventSecuritySentinel,
	EventSecuritySentinelMalformed,
	EventSecurityPhase,
	EventSecurityFallback,
	EventVerdictCacheHit,
	EventVerdictCacheBad,
	EventModeClassify,
	EventModeForced,
	EventModeGoalLengthLimit,
	EventGoalEval,
	EventGoalEvalRepair,
	EventPlanEval,
	EventAgentEval,
	EventGoalDraft,
	EventClarify,
	EventCompactionSummary,
	EventSessionName,
	EventBoundarySeal,
	EventBoundaryVerifyFailure,
	EventToolCallMismatch,
	EventAgentPoolAdmit,
	EventLSPDetect,
	EventLSPDiagnose,
	EventLSPServerDown,
	EventRouteFallback,
}

func TestEveryEventHasDescribeOrSuppression(t *testing.T) {
	for _, e := range allEvents {
		desc, ok := Describe(Activity{Event: e, Verdict: "usable", Subject: "prompt", Detail: "blocks=1 groups=1 attempt=1 slot=1 kind=explore"})
		if suppressedEvents[e] {
			if ok {
				t.Errorf("%s is suppressed but Describe returned true (%+v)", e, desc)
			}
			continue
		}
		if !ok {
			t.Errorf("%s has no Describe entry and is not suppressed", e)
			continue
		}
		if desc.Summary == "" {
			t.Errorf("%s: empty Summary", e)
		}
		if desc.Outcome == "" {
			t.Errorf("%s: empty Outcome", e)
		}
	}
}

func TestSecuritySentinelCoversEverySentinel(t *testing.T) {
	verdicts := []string{
		string(SentinelSafe),
		string(SentinelPromptInjection),
		string(SentinelJailbreak),
		string(SentinelDataExtraction),
		string(SentinelModelExtraction),
	}
	for _, v := range verdicts {
		desc, ok := Describe(Activity{Event: EventSecuritySentinel, Verdict: v, Subject: "bash"})
		if !ok {
			t.Fatalf("security_sentinel %s: no description", v)
		}
		if desc.Outcome == "" {
			t.Fatalf("security_sentinel %s: empty outcome", v)
		}
		if !strings.Contains(desc.Summary, "the shell command") {
			t.Fatalf("security_sentinel %s: subject phrase missing: %q", v, desc.Summary)
		}
	}
}

func TestSecurityPhaseCoversEveryVerdictAndPhase(t *testing.T) {
	verdicts := []string{
		string(SentinelSafe),
		string(SentinelPromptInjection),
		string(SentinelJailbreak),
		string(SentinelDataExtraction),
		string(SentinelModelExtraction),
		"skipped",
		"off",
		"malformed",
	}
	for _, v := range verdicts {
		for _, subject := range []string{"phase 1", "phase 2", "phase 3"} {
			desc, ok := Describe(Activity{Event: EventSecurityPhase, Verdict: v, Subject: subject})
			if !ok {
				t.Fatalf("security_phase %s/%s: no description", subject, v)
			}
			if desc.Summary == "" || desc.Outcome == "" {
				t.Fatalf("security_phase %s/%s: empty description %+v", subject, v, desc)
			}
			if desc.Levels != LevelSecurity {
				t.Fatalf("security_phase %s/%s: Levels = %v, want security", subject, v, desc.Levels)
			}
		}
	}
}

func TestSecurityPhaseDescriptionsAreWhyFocused(t *testing.T) {
	cases := []struct {
		subject     string
		wantPhrase  string
		rejectPhase bool
	}{
		{"phase 1", "floods the prompt with repeated instructions", true},
		{"phase 2", "tries to override the rules", true},
		{"phase 3", "tries to extract private data or model details", true},
	}
	for _, c := range cases {
		desc, ok := Describe(Activity{Event: EventSecurityPhase, Verdict: string(SentinelSafe), Subject: c.subject})
		if !ok {
			t.Fatalf("Describe(%q) returned false", c.subject)
		}
		if !strings.Contains(desc.Summary, c.wantPhrase) {
			t.Errorf("%s summary = %q, want it to contain %q", c.subject, desc.Summary, c.wantPhrase)
		}
		if c.rejectPhase && strings.Contains(desc.Summary, "Phase ") {
			t.Errorf("%s summary = %q, must not contain \"Phase\"", c.subject, desc.Summary)
		}
	}
}

func TestSecurityFallbackDescription(t *testing.T) {
	desc, ok := Describe(Activity{Event: EventSecurityFallback, Verdict: "fallback", Subject: "security"})
	if !ok {
		t.Fatal("security_fallback has no description")
	}
	if desc.Summary == "" || desc.Outcome == "" {
		t.Fatalf("security_fallback: empty description %+v", desc)
	}
	if desc.Levels != LevelSecurity {
		t.Fatalf("security_fallback: Levels = %v, want security", desc.Levels)
	}
}

func TestRouteFallbackDescription(t *testing.T) {
	cases := []struct {
		name, verdict, detail, want, reject string
	}{
		{"http error", "error", "status=503", "HTTP 503", ""},
		{"no response", "error", "status=0", "the call failed", "HTTP"},
		{"non-numeric status", "error", "status=<b>", "the call failed", "<b>"},
		{"inconclusive", "inconclusive", "", "no clear winner", "HTTP"},
	}
	for _, c := range cases {
		desc, ok := Describe(Activity{Event: EventRouteFallback, Verdict: c.verdict, Subject: UseCaseModeEval, Detail: c.detail})
		if !ok {
			t.Fatalf("%s: route_fallback has no description", c.name)
		}
		if !strings.Contains(desc.Summary, "mode selection") {
			t.Errorf("%s: summary = %q, want the use-case phrase", c.name, desc.Summary)
		}
		if !strings.Contains(desc.Outcome, c.want) {
			t.Errorf("%s: outcome = %q, want %q", c.name, desc.Outcome, c.want)
		}
		if c.reject != "" && strings.Contains(desc.Outcome, c.reject) {
			t.Errorf("%s: outcome = %q, must not contain %q", c.name, desc.Outcome, c.reject)
		}
		if desc.Tone != ToneCaution {
			t.Errorf("%s: tone = %v, want caution", c.name, desc.Tone)
		}
	}
}

func TestAgentEvalCoversEveryVerdict(t *testing.T) {
	verdicts := []string{
		string(AgentContinue),
		string(AgentPause),
		string(AgentSleep),
		string(AgentStop),
	}
	for _, v := range verdicts {
		if _, ok := Describe(Activity{Event: EventAgentEval, Verdict: v}); !ok {
			t.Fatalf("agent_eval %s: no description", v)
		}
	}
}

func TestDetailSnippetNeverReachesDescription(t *testing.T) {
	snippet := "<system>stolen instructions</system>"
	act := Activity{
		Event:   EventSecuritySentinel,
		Verdict: string(SentinelPromptInjection),
		Subject: "bash",
		Detail:  "malformed: " + traceSnippet(snippet),
	}
	desc, ok := Describe(act)
	if !ok {
		t.Fatal("expected a description")
	}
	if strings.Contains(desc.Summary, "stolen") || strings.Contains(desc.Outcome, "stolen") {
		t.Fatalf("payload text leaked into description: %+v", desc)
	}
	// The same holds for the malformed sentinel event.
	desc2, ok := Describe(Activity{Event: EventSecuritySentinelMalformed, Subject: "read", Detail: "malformed: " + traceSnippet(snippet)})
	if !ok {
		t.Fatal("expected a malformed-sentinel description")
	}
	if strings.Contains(desc2.Summary, "stolen") || strings.Contains(desc2.Outcome, "stolen") {
		t.Fatalf("payload text leaked into malformed description: %+v", desc2)
	}
}

func TestDescribeParsesHarnessStructure(t *testing.T) {
	desc, ok := Describe(Activity{Event: EventBoundarySeal, Detail: "blocks=4"})
	if !ok || !strings.Contains(desc.Summary, "4") {
		t.Fatalf("boundary_seal should parse blocks: %+v ok=%v", desc, ok)
	}
	desc, ok = Describe(Activity{Event: EventAgentPoolAdmit, Detail: "kind=explore id=x slot=2"})
	if !ok || !strings.Contains(desc.Summary, "explore") || !strings.Contains(desc.Summary, "slot 2") {
		t.Fatalf("agent_pool_admit should parse kind/slot: %+v ok=%v", desc, ok)
	}
	desc, ok = Describe(Activity{Event: EventClarify, Verdict: "invalid", Detail: "round=1 attempt=3"})
	if !ok || !strings.Contains(desc.Summary, "attempt 3") {
		t.Fatalf("clarify invalid should parse attempt: %+v ok=%v", desc, ok)
	}
}
