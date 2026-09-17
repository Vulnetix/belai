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

// record writes one role-manager decision. detail is bounded metadata — never
// untrusted content, never a credential.
func record(event, verdict, tool, detail string, pass int) {
	w := traceWriter()
	if w == nil {
		return
	}
	w.Record(trace.Record{
		Phase:   "rolemanager",
		Event:   event,
		Verdict: verdict,
		Tool:    tool,
		Pass:    pass,
		Detail:  detail,
	})
}
