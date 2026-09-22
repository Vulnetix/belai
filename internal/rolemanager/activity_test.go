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
