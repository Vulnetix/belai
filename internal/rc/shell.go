package rc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Shell lines from the web (belai rc --web-shell).
//
// A web session on a Pix sandbox can run one shell line on its host, two ways:
//
//   - the composer's `!cmd`: Attach is set, so a line whose output the
//     classifier called safe is attached to the model's next turn, as the TUI
//     does for its own `!cmd`;
//   - the console drawer's remote shell: Attach is never set, so the output is
//     shown and recorded and never offered to the model.
//
// Either way the line runs out of band, concurrently with a running turn, under
// the permission rules and OS sandbox the TUI's `!cmd` uses (the ShellRunner
// the daemon supplies), and lands in the transcript as a "shell" entry whose
// meta.shell_id is the command's id, which is how the page finds its result.

// ShellRequest is one shell line to run.
type ShellRequest struct {
	Command string
	// Cwd is the directory the page was showing; the runner confines it to the
	// session's workspace.
	Cwd string
	// PlanMode is whether the session is in plan mode: the TUI refuses a
	// shell line there unless the read-only gate would allow it.
	PlanMode bool
}

// ShellOutcome is what running a line produced.
type ShellOutcome struct {
	// Refused is the harness-worded reason the line did not run (plan mode, a
	// permission rule, a directory outside the workspace). Empty when it ran.
	Refused string
	// Output is what the command printed, as the TUI's shell panel shows it.
	Output string
	// Body is the sanitised copy, which replaces Output when the classifier
	// returned a rewrite; it is what may be attached to a turn.
	Body string
	// Safe is true when the output was classified and found safe. Only a safe
	// output may be attached.
	Safe bool
	// Verdict is the classifier's label when the output was not safe, or why it
	// could not be classified. Empty when Safe.
	Verdict  string
	ExitCode int
	// Cwd is the directory after the line ran, which a `cd` changes.
	Cwd string
	Err error
}

// ShellRunner runs one shell line. The daemon supplies it with the host's
// settings, permission rules, sandbox policy and classifier.
type ShellRunner func(ctx context.Context, req ShellRequest) ShellOutcome

// shellQueue holds shell output waiting for the next turn. It is touched by the
// goroutines that run shell lines and by the turn loop, so it is locked.
type shellQueue struct {
	mu    sync.Mutex
	items []run.Attachment
}

func (q *shellQueue) add(a run.Attachment) {
	q.mu.Lock()
	q.items = append(q.items, a)
	q.mu.Unlock()
}

// drain hands the waiting attachments to a turn exactly once.
func (q *shellQueue) drain() []run.Attachment {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// shellStatus is the entry's status word, in the TUI's vocabulary.
func shellStatus(out ShellOutcome, attach bool) string {
	switch {
	case out.Err != nil:
		return "✗"
	case out.ExitCode != 0:
		return "✗"
	case attach && !out.Safe:
		return "not sent: " + out.Verdict
	default:
		return "✓"
	}
}

// startShell runs c in the background and reports it: the transcript entry
// first, then the ack, so a page that sees the command resolve already has its
// entry.
func startShell(ctx context.Context, o SessionOptions, c sessionsync.RemoteCommand, q *shellQueue, planMode bool) {
	go func() {
		ack := func(status, reason string) {
			if o.AckCommand != nil {
				o.AckCommand(c.ID, status, reason, nil)
			}
		}
		source := "console"
		if c.Attach {
			source = "composer"
		}
		began := time.Now()
		out := o.Shell(ctx, ShellRequest{Command: c.Shell, Cwd: c.Cwd, PlanMode: planMode})
		if out.Refused != "" {
			o.Log.System(fmt.Sprintf("web: shell line refused: %s: %s", out.Refused, c.Shell))
			o.Mirror.Nudge()
			ack(sessionsync.AckRefused, out.Refused)
			return
		}
		content := out.Output
		if out.Err != nil && content == "" {
			content = "failed: " + out.Err.Error()
		}
		meta := map[string]any{
			"command":     c.Shell,
			"shell_id":    c.ID,
			"status":      shellStatus(out, c.Attach),
			"exit_code":   out.ExitCode,
			"duration_ms": time.Since(began).Milliseconds(),
			"cwd":         out.Cwd,
			"source":      source,
			"attached":    false,
		}
		if out.Verdict != "" {
			meta["verdict"] = out.Verdict
		}
		// Only the composer's line, and only a safe output, ever reaches the
		// model. The console's never does, whatever the verdict.
		if c.Attach && out.Err == nil && out.Safe {
			q.add(run.Attachment{Kind: "shell", Label: c.Shell, Body: out.Body})
			meta["attached"] = true
		}
		o.Log.Shell(content, meta)
		o.Mirror.Nudge()
		ack(sessionsync.AckAccepted, "")
	}()
}
