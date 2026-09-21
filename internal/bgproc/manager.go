// Package bgproc supervises long-lived processes started by the user with
// `!!cmd`. Processes run in their own process groups, stream output to a log
// file and to the TUI, and are restarted by a recovery subagent when they
// exit unexpectedly. The recovery subagent has bounded authority: read-only
// tools plus ProcessRestart, with argv[0] pinned and a hard attempt cap.
package bgproc

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/proc"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// State is the lifecycle of a supervised process.
type State string

const (
	StateRunning    State = "running"
	StateExited     State = "exited"
	StateRecovering State = "recovering"
	StateRestarted  State = "restarted"
	StateStopped    State = "stopped"
	StateFailed     State = "failed"
)

// Label returns a human-readable state label.
func (s State) Label() string {
	switch s {
	case StateRunning:
		return "running"
	case StateExited:
		return "exited"
	case StateRecovering:
		return "recovering"
	case StateRestarted:
		return "restarted"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return string(s)
	}
}

// Process is a snapshot of one supervised process. The ID is the stable
// handle used by SubAgentLog across restarts.
type Process struct {
	ID       string
	Name     string
	Command  string
	Dir      string
	LogPath  string
	State    State
	PID      int
	Started  time.Time
	Ended    time.Time
	ExitCode int
	Attempts int
}

// Event is one process-lifecycle event emitted to the TUI.
type Event struct {
	ID      string
	Kind    string // "start", "progress", "exit", "recover", "restart", "fail", "stop", "error"
	Process Process
	Text    string
	Err     error
}

const (
	maxLogBytes        = 16 << 20
	maxLiveTailBytes   = 64 << 20 // not enforced; the UI tee caps at 64 KiB for display
	logRetentionCount  = 50
	displayTailLines   = 32
	recoveryAliveCheck = 10 * time.Second
)

// Manager owns the supervised-process library.
type Manager struct {
	mu       sync.RWMutex
	procs    map[string]*processInstance
	history  map[string]Process
	nextID   int
	events   chan Event
	workdir  string
	cfg      run.Config
	client   *http.Client
	settings config.Settings
	posture  posture.Policy
	live     *posture.Live
	caps     tools.Capabilities
	logsDir  string
}

type processInstance struct {
	mu sync.RWMutex

	id       string
	name     string
	command  string
	dir      string
	logPath  string
	lockPath string
	state    State
	pid      int
	started  time.Time
	ended    time.Time
	exitCode int
	attempts int

	ctx    context.Context
	cancel context.CancelFunc
	log    *cappedLogWriter
	// tail holds the last displayTailLines lines for quick UI inspection.
	tail *ringBuffer
}

type cappedLogWriter struct {
	path      string
	max       int
	mu        sync.Mutex
	f         *os.File
	written   int
	truncated bool
}

type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	size  int
}

// NewManager creates a Manager. It prunes old log files to the newest
// logRetentionCount entries.
func NewManager(workdir string, cfg run.Config, client *http.Client,
	settings config.Settings, pol posture.Policy, caps tools.Capabilities) *Manager {
	logsDir, _ := config.ProcessLogsDir()
	if logsDir == "" {
		logsDir = filepath.Join(workdir, ".vulnetix", "logs")
	}
	_ = os.MkdirAll(logsDir, 0o700)
	pruneLogs(logsDir)

	m := &Manager{
		procs:    make(map[string]*processInstance),
		history:  make(map[string]Process),
		events:   make(chan Event, 256),
		workdir:  workdir,
		cfg:      cfg,
		client:   client,
		settings: settings,
		posture:  pol,
		live:     posture.NewLive(pol, false),
		caps:     caps,
		logsDir:  logsDir,
	}
	return m
}

// SetPosture replaces the policy future recovery subagents are built with and
// pushes it into the manager's live posture holder.
func (m *Manager) SetPosture(p posture.Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posture = p
	if m.live != nil {
		m.live.Set(p, false)
	}
}

// Events returns the process event channel.
func (m *Manager) Events() <-chan Event { return m.events }

// Start launches a supervised process. The name is the library slug; command
// is the full shell command. It returns a snapshot of the launched process.
func (m *Manager) Start(name, command string) (Process, error) {
	if strings.TrimSpace(command) == "" {
		return Process{}, fmt.Errorf("empty command")
	}
	slug := name
	if slug == "" {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return Process{}, fmt.Errorf("empty command")
		}
		slug = filepath.Base(fields[0])
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	lockPath := filepath.Join(m.logsDir, lockName(m.workdir, slug))
	if owner, ok := lockOwner(lockPath); ok && owner != os.Getpid() {
		return Process{}, fmt.Errorf("process %q is already running in another Signet instance (lock %s)", name, lockPath)
	}

	id := fmt.Sprintf("p%d", m.nextID)
	m.nextID++

	logPath := filepath.Join(m.logsDir, fmt.Sprintf("%d-%s-%s.log", time.Now().Unix(), slug, id))
	logW, err := newCappedLogWriter(logPath, maxLogBytes)
	if err != nil {
		return Process{}, fmt.Errorf("create log: %w", err)
	}

	if err := writeLock(lockPath); err != nil {
		_ = logW.Close()
		return Process{}, fmt.Errorf("lock process: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &processInstance{
		id:       id,
		name:     name,
		command:  command,
		dir:      m.workdir,
		logPath:  logPath,
		lockPath: lockPath,
		state:    StateRunning,
		started:  time.Now(),
		ctx:      ctx,
		cancel:   cancel,
		log:      logW,
		tail:     newRingBuffer(displayTailLines),
	}
	m.procs[id] = p

	if err := m.startExecLocked(p); err != nil {
		_ = os.Remove(lockPath)
		delete(m.procs, id)
		return Process{}, err
	}

	m.pushEvent(Event{ID: id, Kind: "start", Process: p.snapshot()})
	return p.snapshot(), nil
}

func (m *Manager) startExecLocked(p *processInstance) error {
	ec := exec.CommandContext(p.ctx, "sh", "-c", p.command)
	ec.Dir = p.dir
	ec.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(ec)

	sink := func(line string) { m.emitProgress(p.id, line) }
	tw := proc.NewLineTee(maxLiveTailBytes, sink)
	mw := io.MultiWriter(p.log, tw)
	ec.Stdout = mw
	ec.Stderr = mw

	if err := ec.Start(); err != nil {
		return err
	}
	p.pid = ec.Process.Pid

	pid := ec.Process.Pid
	go func() {
		err := ec.Wait()
		tw.Flush()
		p.log.Flush()
		code := 0
		if err != nil {
			code = exitCode(err)
		}
		m.handleExit(p.id, pid, code)
	}()
	return nil
}

// Stop terminates a process by ID. A stopped process does not trigger
// recovery.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	p, ok := m.procs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("process %q not found", id)
	}
	p.mu.Lock()
	if p.state == StateStopped || p.state == StateFailed {
		p.mu.Unlock()
		m.mu.Unlock()
		return nil
	}
	p.state = StateStopped
	p.cancel()
	p.mu.Unlock()
	m.mu.Unlock()

	m.releaseLock(p)
	m.pushEvent(Event{ID: id, Kind: "stop", Process: p.snapshot()})
	return nil
}

// Shutdown stops every supervised process. It should be called before Signet
// exits so children in their own process groups are not orphaned.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	for _, p := range m.procs {
		p.state = StateStopped
		p.cancel()
	}
	m.mu.Unlock()
	// Leave a moment for the process group kills to land; processes that are
	// already exiting race with this, which is fine because the lock files are
	// released by Stop or handleExit.
	time.Sleep(100 * time.Millisecond)
}

// List returns snapshots of every supervised process.
func (m *Manager) List() []Process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Process, 0, len(m.procs))
	for _, p := range m.procs {
		out = append(out, p.snapshot())
	}
	return out
}

// Lookup returns a process snapshot by ID.
func (m *Manager) Lookup(id string) (Process, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.procs[id]
	if !ok {
		return Process{}, false
	}
	return p.snapshot(), true
}

func (m *Manager) handleExit(id string, pid, code int) {
	m.mu.Lock()
	p, ok := m.procs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	// A restart may have changed the active pid while this goroutine was
	// waiting for the old process to exit; ignore stale exits.
	if p.pid != pid {
		m.mu.Unlock()
		return
	}
	p.mu.Lock()
	p.ended = time.Now()
	p.exitCode = code

	if p.state == StateStopped {
		snap := p.snapshotLocked()
		p.mu.Unlock()
		m.releaseLock(p)
		m.history[snap.Name] = snap
		delete(m.procs, id)
		m.mu.Unlock()
		m.pushEvent(Event{ID: id, Kind: "exit", Process: snap})
		return
	}

	max := m.settings.Resilience.MaxProcessRecoveriesOr(3)
	if p.attempts >= max {
		p.state = StateFailed
		snap := p.snapshotLocked()
		p.mu.Unlock()
		m.releaseLock(p)
		m.history[snap.Name] = snap
		delete(m.procs, id)
		m.mu.Unlock()
		m.pushEvent(Event{ID: id, Kind: "fail", Process: snap,
			Err: fmt.Errorf("process exited (code %d); recovery attempt cap (%d) reached", code, max)})
		return
	}

	p.state = StateRecovering
	snap := p.snapshotLocked()
	p.mu.Unlock()
	m.mu.Unlock()
	m.pushEvent(Event{ID: id, Kind: "exit", Process: snap,
		Err: fmt.Errorf("process exited unexpectedly with code %d", code)})
	m.pushEvent(Event{ID: id, Kind: "recover", Process: snap})

	go m.runRecovery(id)
}

// RestartProcess restarts the process after a tool call. It counts against
// the recovery attempt cap.
func (m *Manager) RestartProcess(id, command string) error {
	m.mu.Lock()
	p, ok := m.procs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("process %q not found", id)
	}
	p.mu.Lock()
	max := m.settings.Resilience.MaxProcessRecoveriesOr(3)
	if p.attempts >= max {
		p.state = StateFailed
		snap := p.snapshotLocked()
		p.mu.Unlock()
		m.releaseLock(p)
		m.history[snap.Name] = snap
		delete(m.procs, id)
		m.mu.Unlock()
		m.pushEvent(Event{ID: id, Kind: "fail", Process: snap,
			Err: fmt.Errorf("recovery attempt cap (%d) reached", max)})
		return fmt.Errorf("recovery attempt cap (%d) reached", max)
	}
	p.attempts++
	p.command = command
	p.state = StateRunning
	p.started = time.Now()
	p.ended = time.Time{}
	p.exitCode = 0
	if p.cancel != nil {
		p.cancel()
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.log.restartMarker(command)
	if err := m.startExecLocked(p); err != nil {
		p.state = StateExited
		p.mu.Unlock()
		m.mu.Unlock()
		return fmt.Errorf("restart exec: %w", err)
	}
	snap := p.snapshotLocked()
	p.mu.Unlock()
	m.mu.Unlock()
	m.pushEvent(Event{ID: id, Kind: "restart", Process: snap})
	return nil
}

// ProcessByName returns the newest running or historical process record for
// a library slug. Running processes take precedence over history.
func (m *Manager) ProcessByName(name string) (Process, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest Process
	var found bool
	for _, p := range m.procs {
		if p.name == name {
			if !found || p.started.After(latest.Started) {
				latest = p.snapshotLocked()
				found = true
			}
		}
	}
	if found {
		return latest, true
	}
	if h, ok := m.history[name]; ok {
		return h, true
	}
	return Process{}, false
}

// ProcessCommand returns the current command for a process handle.
func (m *Manager) ProcessCommand(id string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.procs[id]
	if !ok {
		return "", false
	}
	return p.command, true
}

// TailByID returns the last n non-empty lines from a process log file looked
// up by its runtime handle id.
func (m *Manager) TailByID(id string, n int) (string, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("process %q not found", id)
	}
	p.log.Flush()
	return tailFile(p.logPath, n)
}

// Tail returns the last n non-empty lines from a process log file. It
// prefers the running or historical record for name and falls back to the
// newest log file in the logs directory whose name contains the slug.
func (m *Manager) Tail(name string, n int) (string, error) {
	p, ok := m.ProcessByName(name)
	if ok && p.LogPath != "" {
		return tailFile(p.LogPath, n)
	}
	m.mu.RLock()
	logsDir := m.logsDir
	m.mu.RUnlock()
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return "", fmt.Errorf("read logs dir: %w", err)
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	prefix := "-" + name + "-"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if !strings.Contains(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(logsDir, e.Name()), modTime: info.ModTime()})
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no log found for %q", name)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	return tailFile(candidates[0].path, n)
}

func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()
	if n <= 0 {
		n = 32
	}
	const maxLineLen = 16 * 1024
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineLen)
	var ring []string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		ring = append(ring, line)
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scan log: %w", err)
	}
	if len(ring) == 0 {
		return "", nil
	}
	return strings.Join(ring, "\n"), nil
}

// LogGrep searches the log file of one process.
func (m *Manager) LogGrep(id, pattern string, maxMatches, context int) (string, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("process %q not found", id)
	}
	p.log.Flush()

	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}

	f, err := os.Open(p.logPath)
	if err != nil {
		return "", fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	const maxResultBytes = 64 * 1024
	type match struct {
		line    int
		matches []int // indices of matches within the line
	}
	var matches []match
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if len(matches) >= maxMatches {
			break
		}
		line := scanner.Text()
		idx := re.FindAllStringIndex(line, -1)
		if len(idx) > 0 {
			matches = append(matches, match{line: lineNum, matches: flatten(idx)})
		}
	}

	// Build output with context windows.
	var buf strings.Builder
	emitted := make(map[int]bool)
	for _, m := range matches {
		start := m.line - context
		if start < 1 {
			start = 1
		}
		end := m.line + context
		if end > lineNum {
			end = lineNum
		}
		for ln := start; ln <= end; ln++ {
			if emitted[ln] {
				continue
			}
			emitted[ln] = true
			line, err := readLine(p.logPath, ln)
			if err != nil {
				continue
			}
			fmt.Fprintf(&buf, "%s:%d: %s\n", id, ln, line)
			if buf.Len() > maxResultBytes {
				return truncateResult(buf.String(), maxResultBytes), nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan log: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func flatten(in [][]int) []int {
	out := make([]int, 0, len(in)*2)
	for _, pair := range in {
		out = append(out, pair...)
	}
	return out
}

func readLine(path string, lineNum int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for i := 1; scanner.Scan(); i++ {
		if i == lineNum {
			return scanner.Text(), nil
		}
	}
	return "", fmt.Errorf("line %d not found", lineNum)
}

func truncateResult(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// Trim back to last newline for cleanliness.
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		cut = cut[:i]
	}
	return cut + fmt.Sprintf("\n… truncated at %d bytes", max)
}

// LogIDs returns the IDs of processes that have log files.
func (m *Manager) LogIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.procs))
	for id := range m.procs {
		out = append(out, id)
	}
	return out
}

// ProcessLog and ProcessControl interface assertions.
var _ tools.ProcessLog = (*Manager)(nil)
var _ tools.ProcessControl = (*Manager)(nil)

func (p *processInstance) snapshot() Process {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshotLocked()
}

func (p *processInstance) snapshotLocked() Process {
	return Process{
		ID:       p.id,
		Name:     p.name,
		Command:  p.command,
		Dir:      p.dir,
		LogPath:  p.logPath,
		State:    p.state,
		PID:      p.pid,
		Started:  p.started,
		Ended:    p.ended,
		ExitCode: p.exitCode,
		Attempts: p.attempts,
	}
}

func (m *Manager) emitProgress(id, line string) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if ok {
		p.tail.add(line)
	}
	m.pushEvent(Event{ID: id, Kind: "progress", Text: line})
}

func (m *Manager) pushEvent(e Event) {
	select {
	case m.events <- e:
	default:
	}
}

func exitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
		return exitErr.ExitCode()
	}
	return 1
}

func newCappedLogWriter(path string, max int) (*cappedLogWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &cappedLogWriter{path: path, max: max, f: f}, nil
}

func (w *cappedLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return len(p), nil
	}
	if w.written+len(p) <= w.max {
		n, err := w.f.Write(p)
		w.written += n
		return n, err
	}
	room := w.max - w.written
	if room > 0 {
		n, _ := w.f.Write(p[:room])
		w.written += n
	}
	marker := []byte(fmt.Sprintf("\n… log truncated at %d bytes\n", w.max))
	_, _ = w.f.Write(marker)
	w.written += len(marker)
	w.truncated = true
	return len(p), nil
}

func (w *cappedLogWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Sync()
}

func (w *cappedLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

func (w *cappedLogWriter) restartMarker(command string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return
	}
	_, _ = w.f.WriteString(fmt.Sprintf("\n--- restart: %s ---\n", command))
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{lines: make([]string, 0, size), size: size}
}

func (r *ringBuffer) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) >= r.size {
		r.lines = append(r.lines[1:], line)
	} else {
		r.lines = append(r.lines, line)
	}
}

func (r *ringBuffer) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

func lockName(workdir, slug string) string {
	h := sha256.Sum256([]byte(workdir))
	return hex.EncodeToString(h[:])[:16] + "-" + slug + ".lock"
}

func lockOwner(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	if pid == os.Getpid() {
		return pid, true
	}
	if processAlive(pid) {
		return pid, true
	}
	_ = os.Remove(path)
	return 0, false
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(os.Signal(nil)) == nil
}

func writeLock(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	_ = f.Close()
	return err
}

func (m *Manager) releaseLock(p *processInstance) {
	if p.lockPath == "" {
		return
	}
	_ = os.Remove(p.lockPath)
}

func pruneLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []os.DirEntry
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".log") {
			files = append(files, e)
		}
	}
	if len(files) <= logRetentionCount {
		return
	}
	// Sort by ModTime oldest first.
	type item struct {
		de   os.DirEntry
		info os.FileInfo
	}
	var items []item
	for _, e := range files {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{de: e, info: info})
	}
	for i := 0; i < len(items)-1; i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].info.ModTime().After(items[j].info.ModTime()) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	toRemove := len(items) - logRetentionCount
	for i := 0; i < toRemove; i++ {
		_ = os.Remove(filepath.Join(dir, items[i].de.Name()))
	}
}
