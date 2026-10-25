package rc

import (
	"strings"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// encodeTrigger is a request's trigger cleaned and encoded for a child's
// environment, or "" when the request has none Belai knows.
func encodeTrigger(t *sessionsync.Trigger) string {
	if c := t.Clean(); c != nil {
		return c.Encode()
	}
	return ""
}

// TriggerEnv is env for a child the daemon starts: any BELAI_SESSION_TRIGGER
// inherited from the daemon's own environment is removed, so a session never
// reports another start's trigger, and trigger is set when there is one. The
// trigger travels in the environment, not argv, because a Pix Call note is the
// user's words and argv is readable by every user of the host.
func TriggerEnv(env []string, trigger string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, calltrace.EnvTrigger+"=") {
			out = append(out, e)
		}
	}
	if trigger != "" {
		out = append(out, calltrace.EnvTrigger+"="+trigger)
	}
	return out
}

// Provenance is the line a session's first turn carries about how it
// started, read from the BELAI_SESSION_TRIGGER value a parent set, or "" when
// there is none. The value is parsed and cleaned again here: the environment
// is only as trusted as the dispatch that filled it.
func Provenance(raw string) (line, encoded string) {
	t := sessionsync.ParseTrigger(raw)
	if t == nil {
		return "", ""
	}
	return t.Sentence(), t.Encode()
}
