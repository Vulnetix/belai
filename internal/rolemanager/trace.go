package rolemanager

import (
	"fmt"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/otel"
	"github.com/vulnetix/belai/internal/trace"
)

var (
	rmTraceOnce sync.Once
	rmTrace     *trace.Writer
)

func traceWriter() *trace.Writer {
	rmTraceOnce.Do(func() { rmTrace = trace.Env() })
	return rmTrace
}

// record writes one role-manager decision to both sinks: the opt-in trace
// file and the in-process observer. detail is bounded metadata — never
// untrusted content, never a credential. The observer is called under a
// recover so a panicking sink cannot break the decision path, and it receives
// only the same bounded fields trace.Record carries.
func record(e Event, verdict, subject, detail string, pass int) {
	recordTimed(e, verdict, subject, detail, pass, "", 0)
}

// recordModel is record with the provider/model identity that produced the
// verdict. It is used by the security classifier paths so the TUI can show
// which classifier ran, rather than always the agent model.
func recordModel(e Event, verdict, subject, detail string, pass int, model string) {
	recordTimed(e, verdict, subject, detail, pass, model, 0)
}

// recordTimed is recordModel with the wall-clock time the decision took. The
// activity is stamped when it is recorded, so a sink that batches rows (the
// TUI persists at turn end) still keeps each row's own time.
func recordTimed(e Event, verdict, subject, detail string, pass int, model string, took time.Duration) {
	// Only the decision's name and its verdict word are exported; the subject
	// and detail stay local.
	otel.Add("belai.role_decisions", 1, otel.S(otel.AttrRole, string(e)), otel.S(otel.AttrVerdict, verdict))
	if w := traceWriter(); w != nil {
		rec := trace.Record{
			Phase:   "rolemanager",
			Event:   string(e),
			Verdict: verdict,
			Tool:    subject,
			Pass:    pass,
			Detail:  detail,
			Model:   model,
		}
		if took > 0 {
			rec.Duration = took.String()
		}
		w.Record(rec)
	}
	if fn := observer.Load(); fn != nil {
		func() {
			defer func() { _ = recover() }()
			(*fn)(Activity{Event: e, Verdict: verdict, Subject: subject, Detail: detail, Pass: pass, Model: model, At: time.Now(), Duration: took})
		}()
	}
}

// RecordSecurityPhase emits one ML classifier phase decision to the activity
// feed. subject is the phase name ("phase 1" / "phase 2" / "phase 3"); verdict
// is the phase's sentinel token, or a status word ("skipped" / "off") for a
// phase that did not run; model is the provider/model identity of the phase
// ("embedded/GuardrailsAI/…", "huggingface/…", "openrouter/typesafe/jev-1.13").
// It is the mlclassify package's hook into the observer, and never carries
// classified payload text.
func RecordSecurityPhase(subject, verdict, model string) {
	RecordSecurityPhaseTimed(subject, verdict, model, 0)
}

// RecordSecurityPhaseTimed is RecordSecurityPhase with the phase's wall-clock
// time (zero when the phase did not run).
func RecordSecurityPhaseTimed(subject, verdict, model string, took time.Duration) {
	recordTimed(EventSecurityPhase, verdict, subject, "", 0, model, took)
}

// RecordSecurityFallback emits a security-fallback event: the Jev security
// classifier could not settle a verdict (an in-band probability, or a
// malformed or missing answer) and handed off to the fallback classifier, the
// agent model. The event carries no model identity of its own: the TUI
// attributes an activity with an empty Model to the agent model, which is
// exactly the model that ruled on the fallback.
func RecordSecurityFallback() {
	recordModel(EventSecurityFallback, "fallback", "security", "", 0, "")
}

// RecordRouteFallback emits a route-fallback event: Jev did not settle which
// model serves useCase, and the defined model serves it instead. A non-nil
// status marks a failed Decisions call and carries its HTTP status (0 when no
// response arrived); a nil status is an inconclusive reply. Only the status
// number is recorded — never the error body, which is server text.
func RecordRouteFallback(useCase string, status *int, model string, took time.Duration) {
	verdict, detail := "inconclusive", ""
	if status != nil {
		verdict, detail = "error", fmt.Sprintf("status=%d", *status)
	}
	recordTimed(EventRouteFallback, verdict, useCase, detail, 0, model, took)
}
