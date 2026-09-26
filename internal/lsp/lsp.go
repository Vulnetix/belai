// Package lsp implements Belai's lightweight language-server diagnostics.
//
// It is a hand-rolled LSP client designed for one purpose only: the harness
// checks the file the model just edited and hands any diagnostics back on the
// same tool result. Servers are detected on PATH, fallback fixed-argv syntax
// checks run when no server is installed, and every message is capped,
// flattened to one line, stripped of control runes, and sealed with a nonce and
// a SHA-256 by the caller.
//
// The package deliberately avoids external LSP protocol libraries: the decoder
// is where the security lives, and bounded Content-Length parsing plus
// dropping unmodelled fields keeps it auditable in-repo.
package lsp

import (
	"context"
	"io"
	"time"
)

// Severity is a diagnostic severity value.
type Severity int

const (
	SeverityError Severity = iota + 1
	SeverityWarning
	SeverityInfo
	SeverityHint
)

// String returns the conventional LSP severity label.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInfo:
		return "info"
	case SeverityHint:
		return "hint"
	default:
		return "unknown"
	}
}

// Status reports the outcome of a Diagnose call.
type Status string

const (
	// StatusReady means a live server was used and returned a report.
	StatusReady Status = "ready"
	// StatusWarming means the server is still indexing; no report is available.
	StatusWarming Status = "warming"
	// StatusUnavailable means no checker is available for this language.
	StatusUnavailable Status = "unavailable"
	// StatusFallback means a fixed-argv syntax checker was used instead of a server.
	StatusFallback Status = "fallback"
	// StatusTimeout means diagnosis exceeded the per-call budget.
	StatusTimeout Status = "timeout"
	// StatusUnsupported means the file extension does not map to a language.
	StatusUnsupported Status = "unsupported"
)

// Row is one diagnostic message after rendering and hardening.
type Row struct {
	Severity Severity
	Line     int
	Col      int
	Source   string
	Message  string
}

// Report is the result of diagnosing one file.
type Report struct {
	Language  string
	Status    Status
	Rows      []Row
	Truncated int
}

// StartFunc spawns a language-server connection. Tests inject this.
type StartFunc func(ctx context.Context, argv []string, dir string) (Conn, error)

// Conn is a language-server connection.
type Conn interface {
	io.ReadWriteCloser
	Wait() error
}

// Options configures a Manager.
type Options struct {
	// Roots are the confinement roots the server may operate under.
	Roots []string
	// Enabled reports whether a language ID is enabled. nil means everything.
	Enabled func(id string) bool
	// Fallback enables fixed-argv syntax checks when no server is available.
	Fallback bool
	// Budget caps each Diagnose call. Zero means 800ms.
	Budget time.Duration
	// MaxLive caps live connections. Zero means 6.
	MaxLive int
	// Servers supplies global-layer binary overrides per language ID.
	Servers map[string]string
	// Start overrides process spawning. nil means real exec.
	Start StartFunc
	// Now overrides the wall clock. nil means time.Now.
	Now func() time.Time
}

// NewManager returns a manager with no warm servers.
func NewManager(o Options) *Manager {
	return newManager(o)
}

// Diagnose checks abs and returns a report. It never returns an error; every
// failure is encoded in Report.Status.
func (m *Manager) Diagnose(ctx context.Context, abs string, content []byte) Report {
	return m.diagnose(ctx, abs, content)
}

// Warm starts language servers for the given language IDs in the background.
func (m *Manager) Warm(langIDs ...string) {
	m.warm(langIDs)
}

// Close shuts down all live connections.
func (m *Manager) Close() error {
	return m.close()
}

const (
	defaultBudget       = 800 * time.Millisecond
	defaultMaxLive      = 6
	initTimeout         = 10 * time.Second
	defaultIdleEviction = 5 * time.Minute
	janitorTick         = 60 * time.Second
	strikeBudgetHalve   = 3
	strikeDisable       = 5
	defaultCacheTTL     = 300 * time.Millisecond
	fallbackTimeout     = 2 * time.Second
	restartCooldown     = 30 * time.Second
)
