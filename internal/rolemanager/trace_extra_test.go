package rolemanager

import (
	"testing"
	"time"
)

// TestRecordSecurityPhaseAndFallbacks covers the exported ML-classifier hooks
// that fan out to the observer with the right event, verdict, subject, model
// identity, and (for the timed form) duration.
func TestRecordSecurityPhaseAndFallbacks(t *testing.T) {
	got := make(chan Activity, 8)
	cancel := SetObserver(func(a Activity) { got <- a })
	defer cancel()

	RecordSecurityPhase("phase 2", string(SentinelJailbreak), "embedded/x")
	RecordSecurityPhaseTimed("phase 1", string(SentinelSafe), "embedded/y", 12*time.Millisecond)
	RecordSecurityFallback()
	status := 503
	RecordRouteFallback(UseCaseModeEval, &status, "m", 3*time.Millisecond)
	RecordRouteFallback(UseCaseGoalEval, nil, "m2", 0)

	want := []Activity{
		{Event: EventSecurityPhase, Verdict: string(SentinelJailbreak), Subject: "phase 2", Model: "embedded/x"},
		{Event: EventSecurityPhase, Verdict: string(SentinelSafe), Subject: "phase 1", Model: "embedded/y", Duration: 12 * time.Millisecond},
		{Event: EventSecurityFallback, Verdict: "fallback", Subject: "security"},
		{Event: EventRouteFallback, Verdict: "error", Subject: UseCaseModeEval, Model: "m", Detail: "status=503", Duration: 3 * time.Millisecond},
		{Event: EventRouteFallback, Verdict: "inconclusive", Subject: UseCaseGoalEval, Model: "m2"},
	}
	for i, w := range want {
		select {
		case a := <-got:
			if a.Event != w.Event || a.Verdict != w.Verdict || a.Subject != w.Subject || a.Model != w.Model || a.Detail != w.Detail || a.Duration != w.Duration {
				t.Fatalf("activity[%d] = %+v, want %+v", i, a, w)
			}
		case <-time.After(time.Second):
			t.Fatalf("activity[%d] not received", i)
		}
	}
}

// TestRecordRouteFallbackNoStatus carries a nil status as "inconclusive", not
// as a zero-value HTTP status.
func TestRecordRouteFallbackNilStatus(t *testing.T) {
	got := make(chan Activity, 1)
	cancel := SetObserver(func(a Activity) { got <- a })
	defer cancel()

	RecordRouteFallback(UseCaseModeEval, nil, "", 0)
	select {
	case a := <-got:
		if a.Verdict != "inconclusive" || a.Detail != "" {
			t.Fatalf("activity = %+v, want inconclusive with empty detail", a)
		}
	case <-time.After(time.Second):
		t.Fatal("activity not received")
	}
}
