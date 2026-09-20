// Package activity is the honest register of every subprocess Signet launches
// on the user's behalf: Vulnetix runs and probes, `!shell` commands, and
// background agents. It is process-agnostic and has no TUI imports and no
// knowledge of Vulnetix; the TUI renders its records in a side drawer.
package activity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind is the source of one registered activity.
type Kind string

const (
	KindVulnetix Kind = "vulnetix"
	KindShell    Kind = "shell"
	KindAgent    Kind = "agent"
)

// State is one activity lifecycle state.
type State string

const (
	StateQueued  State = "queued"
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateKilled  State = "killed"
)

// MaxOutputBytes caps the output a single activity retains, matching
// vulnetixcli.MaxOutputBytes. The collapsed drawer row shows the last
// ringLines lines; the expanded view shows the capped full output.
const MaxOutputBytes = 4 << 20

// ringLines is how much of a running command's output the collapsed row keeps.
const ringLines = 32

// Activity is one process-agnostic record.
type Activity struct {
	ID          string
	Kind        Kind
	Label       string   // harness-composed, e.g. "vulnetix scan", "!git status"
	Argv        []string // exactly what was exec'd, hardening flags included
	Dir         string
	ProjectRoot string // for the triage hand-off
	State       State
	Started     time.Time
	Ended       time.Time
	ExitCode    int
	TimedOut    bool
	Targets     []string // artifact rel paths produced (vulnetix only)
	// Silent marks an internal harness job whose output must never be
	// round-tripped to the model (e.g. the repo-map scan). It still appears in
	// the drawer's honest register.
	Silent bool
	// Quiet suppresses the transcript start/finish lines. The activity still
	// appears in the runs panel (f9). Silent already suppresses the model
	// round-trip; Quiet suppresses the chat noise.
	Quiet bool
}

// ErrNotFound is returned by Kill for an unknown id.
var ErrNotFound = errors.New("activity not found")

// Handle is a registered activity plus its live output buffer and cancel.
type Handle struct {
	Activity Activity
	cancel   context.CancelFunc
	reg      *Registry

	mu        sync.Mutex
	out       bytes.Buffer
	truncated bool
	ring      []string
	ringN     int
}

// Registry is the thread-safe register. Newest-first listing is derived from
// insertion order at List time.
type Registry struct {
	mu     sync.Mutex
	byID   map[string]*Handle
	order  []*Handle
	seq    int
	events chan Activity
}

// NewRegistry returns a registry with a buffered, non-blocking event channel.
func NewRegistry() *Registry {
	return &Registry{
		byID:   map[string]*Handle{},
		events: make(chan Activity, 128),
	}
}

// Add registers a record and returns its handle. A nil cancel means the
// activity cannot be killed.
func (r *Registry) Add(a Activity, cancel context.CancelFunc) *Handle {
	r.mu.Lock()
	r.seq++
	if a.ID == "" {
		a.ID = fmt.Sprintf("act-%d-%d", time.Now().UnixNano(), r.seq)
	}
	if a.Started.IsZero() {
		a.Started = time.Now()
	}
	if a.State == "" {
		a.State = StateQueued
	}
	h := &Handle{Activity: a, cancel: cancel, reg: r}
	r.byID[a.ID] = h
	r.order = append(r.order, h)
	r.mu.Unlock()
	r.emit(h.snapshot())
	return h
}

// Kill cancels one activity and marks it killed. It reports false for an
// unknown or already-terminal id.
func (r *Registry) Kill(id string) error {
	r.mu.Lock()
	h, ok := r.byID[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	r.mu.Unlock()

	h.mu.Lock()
	if h.Activity.State == StateDone || h.Activity.State == StateFailed || h.Activity.State == StateKilled {
		h.mu.Unlock()
		return nil
	}
	h.Activity.State = StateKilled
	h.Activity.Ended = time.Now()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.emit(h.snapshot())
	return nil
}

// Finish marks one activity terminal by id. Unknown ids are ignored.
func (r *Registry) Finish(id string, exitCode int, timedOut bool, err error) {
	r.mu.Lock()
	h, ok := r.byID[id]
	r.mu.Unlock()
	if ok {
		h.Finish(exitCode, timedOut, err)
	}
}

// List returns every activity, newest first. It never blocks.
func (r *Registry) List() []Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Activity, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		out = append(out, r.order[i].snapshot())
	}
	return out
}

// Events returns the registry's change stream. Every mutation (Add, Append,
// Finish, Kill) emits the affected activity's snapshot.
func (r *Registry) Events() <-chan Activity { return r.events }

// Output returns one activity's full capped output, or "".
func (r *Registry) Output(id string) string {
	r.mu.Lock()
	h, ok := r.byID[id]
	r.mu.Unlock()
	if !ok {
		return ""
	}
	return h.Output()
}

// SetTargets records the artifact rel paths an activity produced.
func (h *Handle) SetTargets(targets []string) {
	h.mu.Lock()
	h.Activity.Targets = append([]string(nil), targets...)
	h.mu.Unlock()
	if h.reg != nil {
		h.reg.emit(h.snapshot())
	}
}

// Append records one line of live output.
func (h *Handle) Append(line string) {
	if line == "" {
		return
	}
	h.mu.Lock()
	if !h.truncated {
		if room := MaxOutputBytes - h.out.Len(); room > 0 {
			if len(line)+1 <= room {
				h.out.WriteString(line)
				h.out.WriteByte('\n')
			} else {
				h.out.WriteString(line[:max(room-1, 0)])
				h.out.WriteByte('\n')
				h.truncated = true
			}
		} else {
			h.truncated = true
		}
	}
	h.ring = append(h.ring, line)
	h.ringN++
	if n := len(h.ring) - ringLines; n > 0 {
		h.ring = append(h.ring[:0], h.ring[n:]...)
	}
	h.mu.Unlock()
	if h.reg != nil {
		h.reg.emit(h.snapshot())
	}
}

// Finish marks the activity terminal. err != nil maps to failed (unless the
// context was killed); a zero exit code with no error maps to done.
func (h *Handle) Finish(exitCode int, timedOut bool, err error) {
	h.mu.Lock()
	if h.Activity.State == StateKilled {
		h.mu.Unlock()
		return // a kill already marked it terminal
	}
	h.Activity.Ended = time.Now()
	h.Activity.ExitCode = exitCode
	h.Activity.TimedOut = timedOut
	switch {
	case err != nil:
		h.Activity.State = StateFailed
	default:
		h.Activity.State = StateDone
	}
	h.mu.Unlock()
	if h.reg != nil {
		h.reg.emit(h.snapshot())
	}
}

// Ring returns the last-N-line collapsed view.
func (h *Handle) Ring() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ring...)
}

// Output returns the capped full output.
func (h *Handle) Output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.out.String()
	if h.truncated {
		s += fmt.Sprintf("\n… truncated at %d bytes", MaxOutputBytes)
	}
	return strings.TrimRight(s, "\n")
}

// snapshot copies the activity's public fields without holding the registry
// lock.
func (h *Handle) snapshot() Activity {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.Activity
	a.Targets = append([]string(nil), h.Activity.Targets...)
	a.Argv = append([]string(nil), h.Activity.Argv...)
	return a
}

// emit pushes a snapshot without blocking the caller (the sink goroutine).
func (r *Registry) emit(a Activity) {
	select {
	case r.events <- a:
	default:
	}
}
