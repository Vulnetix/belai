package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/session"
)

// MaxStreamEvents bounds one stream's file, so a process that cannot upload
// (offline, refused credential) does not fill a disk. Events past it are
// counted and dropped.
const MaxStreamEvents = 100_000

// Dir is where a host keeps its stream files, under Belai's global state
// directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, "sync", "audit") }

// Recorder appends one process run's events to its own stream file and chains
// them. It is safe for concurrent use.
type Recorder struct {
	hostname string
	streamID string
	path     string
	now      func() time.Time

	mu      sync.Mutex
	f       *os.File
	w       *bufio.Writer
	seq     int64
	prev    string
	dropped int64
	notify  func()
}

// Open creates this process's stream file under stateDir and returns the
// Recorder. hostname is stamped on every event (a rename is visible in the log).
func Open(stateDir, hostname string) (*Recorder, error) {
	id, err := session.NewID()
	if err != nil {
		return nil, err
	}
	dir := Dir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, strconv.Itoa(os.Getpid())+"."+id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Recorder{hostname: Clean(hostname), streamID: id, path: path, now: time.Now, f: f, w: bufio.NewWriter(f), seq: 0}, nil
}

// StreamID is the uuid the server knows this stream by.
func (r *Recorder) StreamID() string { return r.streamID }

// Path is the stream file the uploader tails.
func (r *Recorder) Path() string { return r.path }

// Dropped counts events refused after MaxStreamEvents.
func (r *Recorder) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// SetNotify installs a callback run after each appended line (the uploader's
// nudge). It must not block.
func (r *Recorder) SetNotify(f func()) {
	r.mu.Lock()
	r.notify = f
	r.mu.Unlock()
}

// Emit cleans f, stamps it and appends it. An unknown kind is dropped. A write
// error drops the event: the audit never fails the work it records.
func (r *Recorder) Emit(f Fact) {
	e, ok := normalize(f)
	if !ok {
		return
	}
	r.mu.Lock()
	if r.f == nil || r.seq >= MaxStreamEvents {
		r.dropped++
		r.mu.Unlock()
		return
	}
	e.Seq, e.At, e.Hostname, e.Prev = r.seq, r.now().UnixMilli(), r.hostname, r.prev
	e.Hash = HashOf(e)
	line, err := json.Marshal(e)
	if err == nil && len(line) < maxLine {
		_, err = r.w.Write(append(line, '\n'))
		if err == nil {
			err = r.w.Flush()
		}
	} else if err == nil {
		err = errors.New("audit: event too large")
	}
	if err != nil {
		// A partial line may be on disk; the stream is unusable from here.
		r.dropped++
		r.mu.Unlock()
		return
	}
	r.seq, r.prev = e.Seq+1, e.Hash
	notify := r.notify
	r.mu.Unlock()
	if notify != nil {
		notify()
	}
}

// Close flushes and closes the file. The file stays until the uploader has sent
// it all, so a later run can finish an interrupted upload.
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil {
		_ = r.w.Flush()
		_ = r.f.Close()
		r.f = nil
	}
}

var current atomic.Pointer[Recorder]

// SetDefault installs the process's Recorder (nil removes it). The entry points
// that start session sync install one; everything else calls Emit.
func SetDefault(r *Recorder) { current.Store(r) }

// Default returns the installed Recorder, nil when there is none.
func Default() *Recorder { return current.Load() }

// Emit records f on the installed Recorder and does nothing without one.
func Emit(f Fact) {
	if r := current.Load(); r != nil {
		r.Emit(f)
	}
}

// Enabled reports whether Emit records anything. An emitter whose facts cost
// work to gather (a git call) checks it first.
func Enabled() bool { return current.Load() != nil }

// hostState is what NoteHost remembers between runs.
type hostState struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
}

// NoteHost records that this machine has been seen, or that its Belai version
// changed, by comparing against the last values this machine reported. The
// first run emits host.first_seen and a later run with a different version
// emits host.version_changed naming the previous one; a rename alone emits
// nothing (every event carries the hostname). The marker file holds two
// identifiers and is replaced atomically.
func NoteHost(stateDir, version, osName, hostname string) {
	if !Enabled() {
		return
	}
	path := filepath.Join(stateDir, "sync", "host-audit.json")
	var prev hostState
	data, err := os.ReadFile(path)
	first := errors.Is(err, os.ErrNotExist)
	if err == nil {
		_ = json.Unmarshal(data, &prev)
	} else if !first {
		return
	}
	next := hostState{Version: Clean(version), Hostname: Clean(hostname)}
	if !first && prev == next {
		return
	}
	if err := writeHostState(path, next); err != nil {
		return
	}
	switch {
	case first:
		Emit(Fact{Kind: HostFirstSeen, ActorKind: ActorHarness, Data: map[string]string{"version": version, "os": osName}})
	case prev.Version != next.Version:
		Emit(Fact{Kind: HostVersionChange, ActorKind: ActorHarness,
			Data: map[string]string{"version": version, "previous": prev.Version, "os": osName}})
	}
}

func writeHostState(path string, s hostState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
