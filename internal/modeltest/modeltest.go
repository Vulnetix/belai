// Package modeltest checks a model selection before /model saves it. Each
// selected model gets a ladder of steps: is the runtime there, are the
// weights on disk (downloading them only after the user confirms the size),
// does the server start, does the model answer, and does its answer make
// sense. A step that fails says why in harness-composed text and offers
// hints the user can act on; steps that can try an alternative (another
// endpoint path, the other protocol, a CPU-only relaunch) do so themselves.
//
// The package has no TUI dependency: the caller receives Events and a
// Report, and decides what to write. Nothing here writes settings.
package modeltest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/decisionserver"
)

// Status is a step's result.
type Status string

const (
	StatusRunning Status = "running"
	StatusOK      Status = "ok"
	StatusWarn    Status = "warn"
	StatusFail    Status = "fail"
	StatusSkip    Status = "skip"
)

// Action is what a hint's key does in the /model panel.
type Action string

const (
	ActRetry      Action = "retry"      // run the same test again
	ActProviders  Action = "providers"  // open the providers view
	ActRedownload Action = "redownload" // delete belai's copy of the weights, then retry
	ActCPU        Action = "cpu"        // retry with the model kept on the CPU
)

// Hint is one thing the user can try. Key is empty for advice with no key
// (install a package, raise a setting).
type Hint struct {
	Key    string
	Text   string
	Action Action
}

// Outcome is a finished step.
type Outcome struct {
	Status  Status
	Detail  string
	Hints   []Hint
	Metrics map[string]string
}

func ok(format string, a ...any) Outcome {
	return Outcome{Status: StatusOK, Detail: fmt.Sprintf(format, a...)}
}

func warn(detail string, hints ...Hint) Outcome {
	return Outcome{Status: StatusWarn, Detail: detail, Hints: hints}
}

func fail(detail string, hints ...Hint) Outcome {
	return Outcome{Status: StatusFail, Detail: detail, Hints: hints}
}

func skip(detail string) Outcome { return Outcome{Status: StatusSkip, Detail: detail} }

// Step is one check.
type Step struct {
	Name string
	Run  func(ctx context.Context, st *State) Outcome
}

// DownloadOffer asks the user to confirm a download.
type DownloadOffer struct {
	Label string // what the weights are for, e.g. "Decider-4B"
	What  string // repo · file, or the ollama model name
	Size  int64  // bytes; 0 when unknown
	Dest  string
}

// Env is what the steps may use.
type Env struct {
	Client   *http.Client
	HFToken  string
	Registry *activity.Registry
	// Confirm asks the user to accept a download; false declines. It may
	// block until the user answers or ctx ends.
	Confirm func(ctx context.Context, o DownloadOffer) bool
	// NGL is the GPU layer count for a local launch: negative offloads all
	// layers (the default), zero keeps the model on the CPU.
	NGL int
	// LocalDeadline bounds a local server's model load; zero uses 120s.
	LocalDeadline time.Duration
}

// State carries results between the steps of one run.
type State struct {
	Env *Env
	// Handle is a decision server the run launched or found.
	Handle *decisionserver.Handle
	// ChatServer is a chat llama-server the run launched.
	ChatServer *LaunchedServer
	// ModelPath is the weights file found or downloaded.
	ModelPath string
	// Fix is a working endpoint the test found when the configured one did
	// not answer; the caller saves it with the selection.
	Fix  *EndpointFix
	emit func(Event)
	step int
}

// LaunchedServer is a chat llama-server the test started.
type LaunchedServer struct {
	Port int
	Stop func() error
}

// EndpointFix is a self-hosted Jev address that answered when the configured
// one did not.
type EndpointFix struct {
	BaseURL string
	Path    string
}

// progress reports download progress for the running step.
func (st *State) progress(done, total int64) {
	if st.emit != nil {
		st.emit(Event{Kind: EventProgress, Index: st.step, Done: done, Total: total})
	}
}

// log reports a line of output for the running step.
func (st *State) log(line string) {
	if st.emit != nil {
		st.emit(Event{Kind: EventLog, Index: st.step, Line: line})
	}
}

// confirm asks the user to accept a download.
func (st *State) confirm(ctx context.Context, o DownloadOffer) bool {
	if st.Env == nil || st.Env.Confirm == nil {
		return false
	}
	if st.emit != nil {
		st.emit(Event{Kind: EventConfirm, Index: st.step, Offer: &o})
	}
	return st.Env.Confirm(ctx, o)
}

// EventKind names an Event.
type EventKind string

const (
	EventStart    EventKind = "start"
	EventDone     EventKind = "done"
	EventProgress EventKind = "progress"
	EventConfirm  EventKind = "confirm"
	EventLog      EventKind = "log"
)

// Event is a progress report from a run.
type Event struct {
	Kind    EventKind
	Index   int
	Name    string
	Outcome Outcome
	Elapsed time.Duration
	Done    int64
	Total   int64
	Offer   *DownloadOffer
	Line    string
}

// StepResult is one step of a finished run.
type StepResult struct {
	Name    string
	Outcome Outcome
	Elapsed time.Duration
}

// Report is a finished run.
type Report struct {
	Steps  []StepResult
	Passed bool
	// Handle, ChatServer and Fix are carried from State for the caller to
	// adopt when it saves, or release when it does not.
	Handle     *decisionserver.Handle
	ChatServer *LaunchedServer
	Fix        *EndpointFix
}

// Failure is the first failed step, if any.
func (r Report) Failure() (StepResult, bool) {
	for _, s := range r.Steps {
		if s.Outcome.Status == StatusFail {
			return s, true
		}
	}
	return StepResult{}, false
}

// Warnings are the steps that passed with a warning.
func (r Report) Warnings() []StepResult {
	var out []StepResult
	for _, s := range r.Steps {
		if s.Outcome.Status == StatusWarn {
			out = append(out, s)
		}
	}
	return out
}

// Release stops any server the run launched. The caller calls it when the
// selection is not saved.
func (r Report) Release() {
	r.Handle.Stop()
	if r.ChatServer != nil && r.ChatServer.Stop != nil {
		_ = r.ChatServer.Stop()
	}
}

// Run executes steps in order. The first failure ends the run; the steps
// after it are reported as skipped. A cancelled ctx fails the running step.
func Run(ctx context.Context, steps []Step, env *Env, emit func(Event)) Report {
	if env == nil {
		env = &Env{}
	}
	if env.Client == nil {
		env.Client = &http.Client{}
	}
	st := &State{Env: env, emit: emit}
	rep := Report{Passed: true}
	failed := false
	for i, s := range steps {
		if failed {
			rep.Steps = append(rep.Steps, StepResult{Name: s.Name, Outcome: skip("not run")})
			continue
		}
		st.step = i
		if emit != nil {
			emit(Event{Kind: EventStart, Index: i, Name: s.Name})
		}
		start := time.Now()
		out := runStep(ctx, s, st)
		el := time.Since(start)
		if ctx.Err() != nil && out.Status != StatusFail {
			out = fail("cancelled")
		}
		rep.Steps = append(rep.Steps, StepResult{Name: s.Name, Outcome: out, Elapsed: el})
		if emit != nil {
			emit(Event{Kind: EventDone, Index: i, Name: s.Name, Outcome: out, Elapsed: el})
		}
		if out.Status == StatusFail {
			failed = true
			rep.Passed = false
		}
	}
	rep.Handle, rep.ChatServer, rep.Fix = st.Handle, st.ChatServer, st.Fix
	return rep
}

// runStep runs one step, turning a panic into a failure: a probe bug must
// cost the save, never the session.
func runStep(ctx context.Context, s Step, st *State) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			out = fail(fmt.Sprintf("the %s check crashed: %v", s.Name, r), retryHint())
		}
	}()
	return s.Run(ctx, st)
}

// Sizes formats a byte count for offers and progress.
func Sizes(n int64) string {
	switch {
	case n <= 0:
		return "unknown size"
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// oneLine flattens and bounds text shown in a step detail.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
