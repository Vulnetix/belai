// Package decisionserver finds or starts the one local llama-server that
// serves the configured decision model (Decider-4B or Plumb-4B). Every belai
// process on the machine shares it: the TUI, headless runs, fleet workers and
// rc sessions.
//
// The rules:
//   - A running server is reused only when it answers /health and lists the
//     catalogue alias of the wanted model in /v1/models.
//   - Weights are never downloaded here. A missing file is ErrWeightsMissing;
//     downloading happens only in a /model test the user confirmed.
//   - Launching takes a file lock and probes again, so two processes never
//     start two servers.
//   - The argv is harness-fixed (localinfer.DecisionArgs): loopback host, the
//     model by path, the alias. The binary comes from PATH. The server runs
//     with the scrubbed environment in its own process group.
//   - Only the process that launched a server stops it (Handle.Owned).
package decisionserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/localinfer"
)

// DefaultPort is the port a first launch tries; a busy port moves the server
// to a free one, and the state file records where it went.
const DefaultPort = 18097

// ActivityLabel is the activity-registry label of the decision server, kept
// distinct from the chat llama-server so stopping one never stops the other.
const ActivityLabel = "llama-server:decision"

var (
	// ErrWeightsMissing means the model file is not on disk.
	ErrWeightsMissing = errors.New("decision model weights are not downloaded")
	// ErrNoBinary means llama-server is not on PATH.
	ErrNoBinary = errors.New("llama-server is not installed")
)

// Options shapes a launch.
type Options struct {
	Registry *activity.Registry
	OnLine   func(string)
	// Deadline bounds the wait for the model to load; zero uses 120s.
	Deadline time.Duration
	// NGL is the GPU layer count: negative offloads all, zero is CPU only.
	NGL int
	// ModelPath overrides the on-disk lookup (tests, a user-chosen file).
	ModelPath string
	Client    *http.Client
}

// Handle is a server this process can use.
type Handle struct {
	BaseURL string
	Port    int
	// Owned is set when this process launched the server; only then does
	// Stop stop it.
	Owned bool
	stop  func() error
}

// Stop stops the server if this process launched it. It is safe to call on
// a borrowed handle (a no-op) and more than once.
func (h *Handle) Stop() {
	if h != nil && h.Owned && h.stop != nil {
		_ = h.stop()
	}
}

// state is the on-disk record of the running server.
type state struct {
	Port  int    `json:"port"`
	Alias string `json:"alias"`
	PID   int    `json:"pid,omitempty"`
}

// RunDir is where local decision servers keep their state, pid and lock
// files: the global state directory's run directory.
func RunDir() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "run"), nil
}

func readState() (state, bool) {
	dir, err := RunDir()
	if err != nil {
		return state{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, "decision-server.json"))
	if err != nil {
		return state{}, false
	}
	var s state
	if json.Unmarshal(b, &s) != nil || s.Port <= 0 || s.Port > 65535 {
		return state{}, false
	}
	return s, true
}

func writeState(s state) {
	dir, err := RunDir()
	if err != nil {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(dir, "decision-server.json"), b, 0o600)
}

// BaseURL is the address the decision server is expected at: the recorded
// port, else DefaultPort. It never launches anything.
func BaseURL() string {
	if s, ok := readState(); ok {
		return rootURL(s.Port)
	}
	return rootURL(DefaultPort)
}

func rootURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// Serving reports whether a server at base answers /health and serves alias.
func Serving(ctx context.Context, client *http.Client, base, alias string) bool {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	get := func(path string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return 0, nil
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b
	}
	if code, _ := get("/health"); code != http.StatusOK {
		return false
	}
	code, body := get("/v1/models")
	if code != http.StatusOK {
		return false
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &models) != nil {
		return false
	}
	for _, m := range models.Data {
		if m.ID == alias {
			return true
		}
	}
	return false
}

// ModelPath returns the on-disk GGUF for m, or "".
func ModelPath(m decisions.LocalModel) string {
	return localinfer.FindModelFile(m.Repo, m.File)
}

// Ensure returns a handle to a server serving m, reusing a running one or
// launching one when the weights are on disk.
func Ensure(ctx context.Context, m decisions.LocalModel, o Options) (*Handle, error) {
	if s, ok := readState(); ok && s.Alias == m.Alias && Serving(ctx, o.Client, rootURL(s.Port), m.Alias) {
		return &Handle{BaseURL: rootURL(s.Port), Port: s.Port}, nil
	}
	path := o.ModelPath
	if path == "" {
		path = ModelPath(m)
	}
	if path == "" {
		return nil, ErrWeightsMissing
	}
	bin, ok := localinfer.LlamaServer()
	if !ok {
		return nil, ErrNoBinary
	}

	dir, err := RunDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	release, err := config.AcquireFileLock(filepath.Join(dir, "decision-server.lock"))
	if err != nil {
		return nil, fmt.Errorf("decision server lock: %w", err)
	}
	defer release()
	// Another process may have launched it while this one waited.
	if s, ok := readState(); ok && s.Alias == m.Alias && Serving(ctx, o.Client, rootURL(s.Port), m.Alias) {
		return &Handle{BaseURL: rootURL(s.Port), Port: s.Port}, nil
	}

	port := DefaultPort
	if s, ok := readState(); ok {
		port = s.Port
	}
	if !PortFree(port) {
		if port, err = localinfer.FreePort(); err != nil {
			return nil, err
		}
	}
	deadline := o.Deadline
	if deadline <= 0 {
		deadline = 120 * time.Second
	}
	args := localinfer.DecisionArgs(localinfer.DecisionServerOptions{ModelPath: path, Alias: m.Alias, Port: port, NGL: o.NGL})
	stop, err := localinfer.Launch(ctx, bin, args, localinfer.BaseURL("127.0.0.1", port), localinfer.LaunchOptions{
		Deadline: deadline,
		Registry: o.Registry,
		Label:    ActivityLabel,
		Pidfile:  filepath.Join(dir, "decision-server.pid"),
		OnLine:   o.OnLine,
		OnPort:   func(p int) { port = p },
	})
	if err != nil {
		return nil, err
	}
	writeState(state{Port: port, Alias: m.Alias})
	return &Handle{BaseURL: rootURL(port), Port: port, Owned: true, stop: stop}, nil
}

// PortFree reports whether a loopback port can be bound now.
func PortFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// Supervisor keeps the process's handle and restarts a server that went
// away, at most once per RecoverEvery.
type Supervisor struct {
	Model decisions.LocalModel
	Opts  Options

	mu     sync.Mutex
	handle *Handle
	last   time.Time
	busy   bool
}

// RecoverEvery rate-limits restarts.
const RecoverEvery = 30 * time.Second

// Adopt records a handle this process obtained elsewhere (the /model test).
func (s *Supervisor) Adopt(h *Handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != nil && s.handle != h {
		s.handle.Stop()
	}
	s.handle = h
}

// Handle returns the current handle, or nil.
func (s *Supervisor) Handle() *Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handle
}

// Recover relaunches the server in the background when it is gone. Calls
// inside RecoverEvery of the last attempt, or while one runs, do nothing.
// Meanwhile decision calls fail as unavailable and their fallback answers.
func (s *Supervisor) Recover() {
	s.mu.Lock()
	if s.busy || time.Since(s.last) < RecoverEvery {
		s.mu.Unlock()
		return
	}
	s.busy, s.last = true, time.Now()
	s.mu.Unlock()
	go func() {
		h, err := Ensure(context.Background(), s.Model, s.Opts)
		s.mu.Lock()
		s.busy = false
		if err == nil {
			if s.handle != nil && s.handle != h {
				s.handle.Stop()
			}
			s.handle = h
		}
		s.mu.Unlock()
	}()
}

// Stop stops the server if this process owns it.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	h := s.handle
	s.handle = nil
	s.mu.Unlock()
	h.Stop()
}

// Describe is a one-line, harness-composed description of a launch error for
// notices: never the server's log text beyond its class.
func Describe(err error) string {
	switch {
	case errors.Is(err, ErrWeightsMissing):
		return "decision model not downloaded; open /model and select it to download"
	case errors.Is(err, ErrNoBinary):
		return "llama-server is not installed; security checks use the agent model"
	}
	var le *localinfer.LaunchError
	if errors.As(err, &le) {
		switch le.Class {
		case localinfer.LogUpgrade:
			return "llama-server is too old for this decision model; update llama.cpp"
		case localinfer.LogOOM:
			return "the decision model does not fit in memory"
		case localinfer.LogGPU:
			return "llama-server's GPU backend failed; retest in /model to fall back to the CPU"
		case localinfer.LogCorrupt:
			return "the decision model file is damaged; retest in /model to download it again"
		}
		return "decision server did not start"
	}
	return "decision server unavailable: " + strings.TrimSpace(firstLine(err.Error()))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Supervisor{}
)

// Shared returns this process's supervisor for m. The decision transport
// asks it to Recover when the server refuses a connection; the TUI and the
// CLI adopt the handle they launched into it, so exit stops what they own.
func Shared(m decisions.LocalModel) *Supervisor {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if s, ok := shared[m.ID]; ok {
		return s
	}
	s := &Supervisor{Model: m, Opts: Options{NGL: -1}}
	shared[m.ID] = s
	return s
}

// StopAll stops every server this process launched.
func StopAll() {
	sharedMu.Lock()
	list := make([]*Supervisor, 0, len(shared))
	for _, s := range shared {
		list = append(list, s)
	}
	sharedMu.Unlock()
	for _, s := range list {
		s.Stop()
	}
}
