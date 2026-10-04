package rc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
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
	// Started, when set, is called once the line has passed every gate and
	// the process is about to run, so the page can show it as running.
	Started func()
	// Stream, when set, receives the line's output as it is produced, in whole
	// lines, so the page can show a long command as it goes.
	Stream func(chunk string)
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

// Streaming limits. A slice of output is one transcript entry, so slices are
// batched, and a command that prints without end stops being mirrored at the
// cap (the final entry still carries the head and tail of what it printed).
const (
	shellStreamEvery = 300 * time.Millisecond
	shellStreamBatch = 8 << 10
	shellStreamMax   = 256 << 10
)

// shellStreamer batches a running line's output into "shell_out" entries.
type shellStreamer struct {
	log    *turnlog.Log
	nudge  func()
	id     string
	mu     sync.Mutex
	buf    strings.Builder
	timer  *time.Timer
	n      int
	sent   int
	capped bool
	closed bool
}

func (s *shellStreamer) write(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.capped || chunk == "" {
		return
	}
	s.buf.WriteString(chunk)
	s.buf.WriteByte('\n')
	if s.buf.Len() >= shellStreamBatch {
		s.flushLocked()
		return
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(shellStreamEvery, s.flush)
	}
}

func (s *shellStreamer) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
}

func (s *shellStreamer) flushLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.buf.Len() == 0 || s.closed {
		return
	}
	text := s.buf.String()
	s.buf.Reset()
	s.sent += len(text)
	if s.sent > shellStreamMax {
		s.capped = true
		text += "… output continues, the finished line keeps its start and end\n"
	}
	s.log.ShellOut(text, map[string]any{"shell_id": s.id, "n": s.n})
	s.n++
	s.nudge()
}

// close flushes what is left and stops further entries.
func (s *shellStreamer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
	s.closed = true
}

// analysisPrompt is the turn a failed composer line raises, so the model reads
// the error and offers a fix without the user having to ask. The command is
// quoted, and the output arrives as an attachment, never inside this text.
func analysisPrompt(command string, out ShellOutcome, attached bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The shell line %q that I ran from the web composer failed", command)
	if out.Err != nil {
		fmt.Fprintf(&b, " before it could finish (%s).", out.Err.Error())
	} else {
		fmt.Fprintf(&b, " with exit status %d.", out.ExitCode)
	}
	if attached {
		b.WriteString(" Its output is attached.")
	} else {
		verdict := out.Verdict
		if verdict == "" {
			verdict = "it was not classified as safe"
		}
		fmt.Fprintf(&b, " Its output was withheld from you (%s).", verdict)
	}
	b.WriteString(" Work out why it failed. Fix it with your tools where that is within your permissions, or run a check that narrows it down. If a fix needs something only I can give (a login, a token, an approval), say exactly what to do. Keep the answer short.")
	return b.String()
}

// startShell runs c in the background and reports it. The transcript gets a
// "shell_run" entry when the line starts, "shell_out" slices while it runs, and
// the final "shell" entry, which carries the result; the ack follows the final
// entry, so a page that sees the command resolve already has its entry. A
// composer line that failed also raises a turn (analyse) so the model reads the
// error and proposes a fix.
func startShell(ctx context.Context, o SessionOptions, c sessionsync.RemoteCommand, q *shellQueue, planMode bool, analyse func(sessionsync.RemotePrompt)) {
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
		stream := &shellStreamer{log: o.Log, nudge: o.Mirror.Nudge, id: c.ID}
		req := ShellRequest{Command: c.Shell, Cwd: c.Cwd, PlanMode: planMode, Stream: stream.write}
		req.Started = func() {
			o.Log.ShellRun(map[string]any{
				"shell_id": c.ID,
				"command":  c.Shell,
				"cwd":      c.Cwd,
				"source":   source,
				"attach":   c.Attach,
			})
			o.Mirror.Nudge()
		}
		out := o.Shell(ctx, req)
		stream.close()
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
		attached := c.Attach && out.Err == nil && out.Safe
		if attached {
			q.add(run.Attachment{Kind: "shell", Label: c.Shell, Body: out.Body})
			meta["attached"] = true
		}
		failed := out.Err != nil || out.ExitCode != 0
		raise := c.Attach && failed && analyse != nil
		if raise {
			meta["analysing"] = true
		}
		o.Log.Shell(content, meta)
		o.Mirror.Nudge()
		ack(sessionsync.AckAccepted, "")
		if raise {
			analyse(sessionsync.RemotePrompt{Content: analysisPrompt(c.Shell, out, attached), Origin: "shell"})
		}
	}()
}
