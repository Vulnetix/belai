package rc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// DefaultMax is how many sessions a daemon runs at once unless told otherwise.
const DefaultMax = 3

// DefaultIdle is how long a started session waits for a follow-up prompt
// before it ends on its own.
const DefaultIdle = 30 * time.Minute

// Options configure the daemon.
type Options struct {
	// Exe is the belai binary each session runs as `belai rc-session`.
	Exe    string
	Client *sessionsync.Client
	HostID string
	Host   sessionsync.Host
	Dirs   []Dir
	Max    int
	Idle   time.Duration
	URL    string
	// Out receives one line per event (stderr, or the log when detached).
	Out     io.Writer
	LogPath string

	HeartbeatEvery time.Duration // 15s
	PollWait       time.Duration // 25s
	// Start replaces the child spawn in tests.
	Start func(c Child) (pid int, wait func() error, err error)
}

// Child is one session to start.
type Child struct {
	Exe, Cwd, Dispatch, SessionID, Mode, Prompt, LogPath string
	Idle                                                 time.Duration
}

// Daemon is a running `belai rc`.
type Daemon struct {
	o Options

	mu       sync.Mutex
	sessions map[string]*child
	online   bool
	lastErr  string
	started  time.Time
	wg       sync.WaitGroup
}

type child struct {
	RunningSession
}

// New validates options and builds a daemon.
func New(o Options) (*Daemon, error) {
	if o.Client == nil || o.HostID == "" {
		return nil, errors.New("rc: a sync client and host id are required")
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	if o.Idle <= 0 {
		o.Idle = DefaultIdle
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 15 * time.Second
	}
	if o.PollWait <= 0 {
		o.PollWait = 25 * time.Second
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Start == nil {
		o.Start = startChild
	}
	return &Daemon{o: o, sessions: map[string]*child{}, started: time.Now()}, nil
}

func (d *Daemon) logf(format string, args ...any) {
	fmt.Fprintf(d.o.Out, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Run serves until ctx ends, then stops every session it started and tells
// the website the host is offline.
func (d *Daemon) Run(ctx context.Context) error {
	if r, live := ReadRecord(); live && r.PID != os.Getpid() {
		return fmt.Errorf("belai rc is already running (pid %d); stop it with `belai rc --stop`", r.PID)
	}
	d.writeRecord()
	defer removeRecord(os.Getpid())

	d.register(ctx)
	d.logf("remote control on · %d director%s offered · up to %d sessions", len(d.o.Dirs), plural(len(d.o.Dirs), "y", "ies"), d.o.Max)
	d.logf("start sessions at %s", d.o.URL)

	go d.heartbeat(ctx)
	d.poll(ctx)

	d.shutdown()
	return nil
}

// register advertises the host, retrying until it succeeds or ctx ends.
func (d *Daemon) register(ctx context.Context) {
	h := d.o.Host
	h.RC = &sessionsync.RCInfo{MaxSessions: d.o.Max}
	for _, dir := range d.o.Dirs {
		h.RC.Dirs = append(h.RC.Dirs, sessionsync.RCDir{Path: dir.Path, Name: dir.Name, Source: dir.Source})
	}
	backoff := time.Second
	for {
		err := d.o.Client.PutHost(ctx, d.o.HostID, h)
		d.setOnline(err)
		if err == nil || ctx.Err() != nil {
			return
		}
		d.logf("register: %v (retrying in %s)", err, backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, time.Minute)
	}
}

func (d *Daemon) heartbeat(ctx context.Context) {
	t := time.NewTicker(d.o.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := d.o.Client.RCHeartbeat(ctx, d.o.HostID, d.running())
		if errors.Is(err, sessionsync.ErrNotFound) {
			// The server lost the host (or never had it): advertise again.
			d.register(ctx)
			err = nil
		}
		if err != nil && ctx.Err() == nil {
			d.logf("heartbeat: %v", err)
		}
		d.setOnline(err)
		d.writeRecord()
	}
}

func (d *Daemon) poll(ctx context.Context) {
	var backoff time.Duration
	for ctx.Err() == nil {
		reqs, err := d.o.Client.Dispatches(ctx, d.o.HostID, d.o.PollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(max(2*backoff, 2*time.Second), time.Minute)
			d.logf("poll: %v (retrying in %s)", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		for _, r := range reqs {
			d.handle(ctx, r)
		}
	}
}

// handle applies one request. Every field is untrusted.
func (d *Daemon) handle(ctx context.Context, r sessionsync.Dispatch) {
	switch r.Kind {
	case "start":
		sid, reason := d.start(r)
		if reason != "" {
			d.logf("refused session in %s: %s", r.Cwd, reason)
			d.ack(ctx, r.ID, sessionsync.DispatchRefused, "", reason)
			return
		}
		d.ack(ctx, r.ID, sessionsync.DispatchStarted, sid, "")
	case "stop":
		if d.stop(r.SessionID) {
			d.logf("stopping session %s", short(r.SessionID))
			d.ack(ctx, r.ID, sessionsync.DispatchStopped, r.SessionID, "")
			return
		}
		d.ack(ctx, r.ID, sessionsync.DispatchRefused, "", "that session is not running under belai rc on this host")
	default:
		d.ack(ctx, r.ID, sessionsync.DispatchRefused, "", "this Belai does not understand that request; update Belai on the host")
	}
}

func (d *Daemon) ack(ctx context.Context, id, status, sid, reason string) {
	if err := d.o.Client.AckDispatch(ctx, id, status, sid, reason); err != nil && ctx.Err() == nil {
		d.logf("ack %s: %v", short(id), err)
	}
}

// start validates a start request and spawns its session. It returns the
// new session id, or the reason it refused.
func (d *Daemon) start(r sessionsync.Dispatch) (string, string) {
	cwd, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "this host does not offer that directory"
	}
	switch r.Mode {
	case "", "agent", "plan", "goal":
	default:
		return "", "unknown mode " + fmt.Sprintf("%q", r.Mode)
	}
	prompt := sessionsync.CleanPrompt(r.Prompt)
	if prompt == "" {
		return "", "the prompt was empty after cleaning"
	}
	d.mu.Lock()
	if len(d.sessions) >= d.o.Max {
		n := len(d.sessions)
		d.mu.Unlock()
		return "", fmt.Sprintf("this host is already running %d of %d sessions", n, d.o.Max)
	}
	d.mu.Unlock()

	sid, err := session.NewID()
	if err != nil {
		return "", "could not mint a session id"
	}
	c := Child{
		Exe: d.o.Exe, Cwd: cwd, Dispatch: r.ID, SessionID: sid, Mode: r.Mode, Prompt: prompt,
		Idle: d.o.Idle, LogPath: d.sessionLog(sid),
	}
	pid, wait, err := d.o.Start(c)
	if err != nil {
		return "", "could not start Belai: " + err.Error()
	}
	d.mu.Lock()
	d.sessions[sid] = &child{RunningSession{ID: sid, Dispatch: r.ID, Cwd: cwd, PID: pid, Started: time.Now()}}
	d.mu.Unlock()
	d.writeRecord()
	d.logf("started session %s in %s (pid %d)", short(sid), cwd, pid)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		err := wait()
		d.mu.Lock()
		delete(d.sessions, sid)
		d.mu.Unlock()
		d.writeRecord()
		if err != nil {
			d.logf("session %s ended: %v (log: %s)", short(sid), err, c.LogPath)
		} else {
			d.logf("session %s ended", short(sid))
		}
	}()
	return sid, ""
}

func (d *Daemon) stop(sid string) bool {
	d.mu.Lock()
	c, ok := d.sessions[strings.ToLower(sid)]
	d.mu.Unlock()
	if !ok {
		return false
	}
	_ = proc.Terminate(c.PID)
	return true
}

// shutdown stops every session and marks the host offline.
func (d *Daemon) shutdown() {
	d.mu.Lock()
	var pids []int
	for _, c := range d.sessions {
		pids = append(pids, c.PID)
	}
	d.mu.Unlock()
	if len(pids) > 0 {
		d.logf("stopping %d session%s", len(pids), plural(len(pids), "", "s"))
	}
	for _, pid := range pids {
		_ = proc.Terminate(pid)
	}
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		for _, pid := range pids {
			_ = proc.Kill(pid)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.o.Client.RCOffline(ctx, d.o.HostID); err != nil {
		d.logf("offline: %v", err)
	}
	d.logf("remote control off")
}

func (d *Daemon) running() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

func (d *Daemon) setOnline(err error) {
	d.mu.Lock()
	d.online = err == nil
	d.lastErr = ""
	if err != nil {
		d.lastErr = err.Error()
	}
	d.mu.Unlock()
}

func (d *Daemon) writeRecord() {
	d.mu.Lock()
	r := Record{
		PID: os.Getpid(), Started: d.started, Beat: time.Now(), HostID: d.o.HostID, URL: d.o.URL,
		Max: d.o.Max, Dirs: d.o.Dirs, LogPath: d.o.LogPath, Online: d.online, Error: d.lastErr,
	}
	for _, c := range d.sessions {
		r.Sessions = append(r.Sessions, c.RunningSession)
	}
	d.mu.Unlock()
	sort.Slice(r.Sessions, func(i, j int) bool { return r.Sessions[i].Started.Before(r.Sessions[j].Started) })
	_ = writeRecord(r)
}

func (d *Daemon) sessionLog(sid string) string {
	dir, err := StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sessions", sid+".log")
}

// startChild runs `belai rc-session` in the session's directory, in its own
// process group so a stop reaches its tools too. The prompt goes over stdin,
// never argv, so it stays out of the process list.
func startChild(c Child) (int, func() error, error) {
	args := []string{"rc-session", "-dispatch", c.Dispatch, "-session-id", c.SessionID, "-idle", c.Idle.String()}
	if c.Mode != "" {
		args = append(args, "-mode", c.Mode)
	}
	cmd := exec.Command(c.Exe, args...)
	cmd.Dir = c.Cwd
	cmd.Stdin = strings.NewReader(c.Prompt)
	var logFile *os.File
	if c.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o700); err == nil {
			if f, err := os.OpenFile(c.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				logFile = f
				cmd.Stdout, cmd.Stderr = f, f
			}
		}
	}
	proc.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return 0, nil, err
	}
	return cmd.Process.Pid, func() error {
		err := cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		return err
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
