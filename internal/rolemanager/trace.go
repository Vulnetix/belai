package rolemanager

import (
	"sync"

	"github.com/vulnetix/signet/internal/trace"
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
	recordModel(e, verdict, subject, detail, pass, "")
}

// recordModel is record with the provider/model identity that produced the
// verdict. It is used by the security classifier paths so the TUI can show
// which classifier ran, rather than always the agent model.
func recordModel(e Event, verdict, subject, detail string, pass int, model string) {
	if w := traceWriter(); w != nil {
		w.Record(trace.Record{
			Phase:   "rolemanager",
			Event:   string(e),
			Verdict: verdict,
			Tool:    subject,
			Pass:    pass,
			Detail:  detail,
			Model:   model,
		})
	}
	if fn := observer.Load(); fn != nil {
		func() {
			defer func() { _ = recover() }()
			(*fn)(Activity{Event: e, Verdict: verdict, Subject: subject, Detail: detail, Pass: pass, Model: model})
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
	recordModel(EventSecurityPhase, verdict, subject, "", 0, model)
}
