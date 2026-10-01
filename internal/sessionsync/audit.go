package sessionsync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/proc"
)

// ─────────────────────────────────────────────────────────────────────────
// audit.go — the audit log's upload. It is the session mirror's machinery for a
// second stream: the Recorder (internal/audit) appends hash-chained facts to its
// own file, and an AuditSyncer tails that file and uploads each line with its
// line index, so an upload can be retried or repeated without duplicating
// anything and a process that restarts resumes from the server's cursor.
//
// Unlike the session mirror this channel carries facts only. The syncer never
// composes an event: it sends the lines the Recorder wrote, and the server
// re-validates and re-chains them. See docs/audit.md.
// ─────────────────────────────────────────────────────────────────────────

// AuditCursor is the server's high-water mark for a stream.
type AuditCursor struct {
	LastSeq  int64  `json:"lastSeq"`
	LastHash string `json:"lastHash"`
	// ChainOK is false once an upload did not continue the chain.
	ChainOK bool `json:"chainOk"`
}

// PutAuditStream registers this process's stream and returns the server's
// cursor (-1 when it holds nothing yet).
func (c *Client) PutAuditStream(ctx context.Context, hostID, streamID string) (AuditCursor, error) {
	var out AuditCursor
	err := c.do(ctx, http.MethodPut, auditPath(hostID, streamID), nil, &out, requestTimeout)
	return out, err
}

// PostAudit uploads events; repeats are ignored server-side.
func (c *Client) PostAudit(ctx context.Context, hostID, streamID string, events []audit.Event) (AuditCursor, error) {
	var out AuditCursor
	err := c.doBody(ctx, http.MethodPost, auditPath(hostID, streamID), map[string]any{"events": events}, &out, requestTimeout, true)
	return out, err
}

func auditPath(hostID, streamID string) string {
	return "/hosts/" + url.PathEscape(hostID) + "/audit/" + url.PathEscape(streamID)
}

// Audit upload limits. The server takes at most 200 events and 8 MiB a batch.
const (
	maxAuditBatchEvents = 100
	maxAuditBatchBytes  = 2 << 20
	// maxSweepFiles bounds the orphaned streams one sweep uploads.
	maxSweepFiles = 20
)

// AuditOptions configures an AuditSyncer. Zero intervals take the defaults.
type AuditOptions struct {
	Client   *Client
	HostID   string
	Host     Host
	Recorder *audit.Recorder
	// StateDir is Belai's global state directory, where orphaned streams of
	// crashed processes are found and finished.
	StateDir string

	TickEvery  time.Duration // how often the file is re-read without a nudge (2s)
	SweepEvery time.Duration // how often orphaned streams are looked for (10m)
	BackoffMin time.Duration // first retry delay after a failure (2s)
}

// AuditStatus is a snapshot for /sync status.
type AuditStatus struct {
	LastSeq   int64 // highest event the server holds for this stream
	ChainOK   bool
	LastError string
	LastOK    time.Time
}

// AuditSyncer uploads one process's audit stream. All network I/O runs on its
// own goroutine; Nudge never blocks.
type AuditSyncer struct {
	opts AuditOptions

	nudge    chan struct{}
	closing  chan time.Duration
	stopped  chan struct{}
	cancel   context.CancelFunc
	closeOne sync.Once

	mu     sync.Mutex
	status AuditStatus
}

// NewAudit builds an AuditSyncer and points the Recorder's notify at it. Call
// Start to run it.
func NewAudit(opts AuditOptions) *AuditSyncer {
	if opts.TickEvery <= 0 {
		opts.TickEvery = 2 * time.Second
	}
	if opts.SweepEvery <= 0 {
		opts.SweepEvery = 10 * time.Minute
	}
	if opts.BackoffMin <= 0 {
		opts.BackoffMin = 2 * time.Second
	}
	s := &AuditSyncer{
		opts:    opts,
		nudge:   make(chan struct{}, 1),
		closing: make(chan time.Duration, 1),
		stopped: make(chan struct{}),
		status:  AuditStatus{LastSeq: -1, ChainOK: true},
	}
	if opts.Recorder != nil {
		opts.Recorder.SetNotify(s.Nudge)
	}
	return s
}

// StartAudit gives this process an audit Recorder, installs it as the default
// (so audit.Emit records), notes the host's first sighting or version change and
// starts uploading. It returns nil when the process already has a Recorder or
// the stream file cannot be created: the audit is best effort and never stops the
// work it would record. Close the result at exit.
func StartAudit(ctx context.Context, c *Client, hostID string, h Host, stateDir string) *AuditSyncer {
	if c == nil || hostID == "" || stateDir == "" || audit.Enabled() {
		return nil
	}
	rec, err := audit.Open(stateDir, h.Hostname)
	if err != nil {
		return nil
	}
	audit.SetDefault(rec)
	audit.NoteHost(stateDir, h.BelaiVersion, h.OS, h.Hostname)
	s := NewAudit(AuditOptions{Client: c, HostID: hostID, Host: h, Recorder: rec, StateDir: stateDir})
	s.Start(ctx)
	return s
}

// Start runs the upload loop.
func (s *AuditSyncer) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
}

// Nudge asks for an upload after the Recorder appended an event.
func (s *AuditSyncer) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Status returns a snapshot.
func (s *AuditSyncer) Status() AuditStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Close uploads what the file holds and stops, waiting at most timeout. A
// stream that uploaded completely is removed; one that did not stays for the
// next run's sweep.
func (s *AuditSyncer) Close(timeout time.Duration) {
	if s == nil {
		return
	}
	s.closeOne.Do(func() {
		s.closing <- timeout
		select {
		case <-s.stopped:
		case <-time.After(timeout + 500*time.Millisecond):
		}
		if s.cancel != nil {
			s.cancel()
		}
		if s.opts.Recorder != nil && audit.Default() == s.opts.Recorder {
			audit.SetDefault(nil)
		}
	})
}

func (s *AuditSyncer) setErr(err error) {
	s.mu.Lock()
	s.status.LastError = err.Error()
	s.mu.Unlock()
}

// auditTail is the upload state for one stream file. offset and seq point at
// the first line not yet known to be on the server.
type auditTail struct {
	path       string
	id         string
	registered bool
	serverLast int64
	offset     int64
	seq        int64
}

func (s *AuditSyncer) run(ctx context.Context) {
	defer close(s.stopped)
	rec := s.opts.Recorder
	if rec == nil {
		return
	}
	cur := &auditTail{path: rec.Path(), id: rec.StreamID(), serverLast: -1}
	hostOK := false
	var backoff time.Duration
	var retryAt, lastSweep time.Time
	tick := time.NewTicker(s.opts.TickEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case timeout := <-s.closing:
			fctx, cancel := context.WithTimeout(context.Background(), timeout)
			rec.Close()
			if err := s.step(fctx, cur, &hostOK, true); err == nil && drained(cur) {
				_ = os.Remove(cur.path)
			}
			cancel()
			return
		case <-s.nudge:
			coalesce(ctx, s.nudge)
		case <-tick.C:
		}
		if time.Now().Before(retryAt) {
			continue
		}
		if err := s.step(ctx, cur, &hostOK, true); err != nil {
			if errors.Is(err, ErrNotFound) {
				// The server no longer knows the host: register again.
				cur.registered = false
				hostOK = false
			}
			backoff = min(max(2*backoff, s.opts.BackoffMin), time.Minute)
			retryAt = time.Now().Add(backoff)
			s.setErr(err)
			continue
		}
		backoff = 0
		if time.Since(lastSweep) >= s.opts.SweepEvery {
			lastSweep = time.Now()
			if err := s.sweep(ctx, cur.id, &hostOK); err != nil && ctx.Err() == nil {
				s.setErr(err)
			}
		}
	}
}

// coalesce holds a nudged upload briefly so the events written right after it
// ride the same batch.
func coalesce(ctx context.Context, nudge <-chan struct{}) {
	t := time.NewTimer(nudgeCoalesce)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-nudge:
		case <-t.C:
			return
		}
	}
}

// step registers what is not yet registered and uploads the stream's new lines.
func (s *AuditSyncer) step(ctx context.Context, t *auditTail, hostOK *bool, live bool) error {
	if _, err := os.Stat(t.path); err != nil {
		return nil
	}
	if !*hostOK {
		if err := s.opts.Client.PutHost(ctx, s.opts.HostID, s.opts.Host); err != nil {
			return err
		}
		*hostOK = true
	}
	if !t.registered {
		cur, err := s.opts.Client.PutAuditStream(ctx, s.opts.HostID, t.id)
		if err != nil {
			return err
		}
		t.registered, t.serverLast, t.offset, t.seq = true, cur.LastSeq, 0, 0
		if live {
			s.note(cur)
		}
	}
	cur, err := s.upload(ctx, t)
	if err != nil {
		return err
	}
	if live && cur != nil {
		s.note(*cur)
	}
	if live {
		s.mu.Lock()
		s.status.LastError = ""
		s.status.LastOK = time.Now()
		s.mu.Unlock()
	}
	return nil
}

func (s *AuditSyncer) note(c AuditCursor) {
	s.mu.Lock()
	s.status.LastSeq, s.status.ChainOK = c.LastSeq, c.ChainOK
	s.mu.Unlock()
}

// upload sends every complete line after the server's mark, in order. The
// offset advances only past lines the server has acknowledged, so a failure
// re-reads and re-sends, which is harmless: the server ignores repeats. It
// returns the last cursor the server reported, nil when nothing was sent.
func (s *AuditSyncer) upload(ctx context.Context, t *auditTail) (*AuditCursor, error) {
	f, err := os.Open(t.path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	pos, seq := t.offset, t.seq
	var batch []audit.Event
	var last *AuditCursor
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		cur, err := s.opts.Client.PostAudit(ctx, s.opts.HostID, t.id, batch)
		if err != nil {
			return err
		}
		last = &cur
		t.offset, t.seq = pos, seq
		if cur.LastSeq > t.serverLast {
			t.serverLast = cur.LastSeq
		}
		batch, size = batch[:0], 0
		return nil
	}
	for {
		line, rerr := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			// EOF, or a line still being written: wait for its newline.
			break
		}
		pos += int64(len(line))
		idx := seq
		seq++
		if idx <= t.serverLast {
			if len(batch) == 0 {
				t.offset, t.seq = pos, seq
			}
			continue
		}
		var e audit.Event
		if err := json.Unmarshal(line, &e); err != nil {
			// Not an event this Recorder wrote. The line keeps its place, so the
			// server sees a gap in the chain and flags the stream.
			if len(batch) == 0 {
				t.offset, t.seq = pos, seq
			}
			continue
		}
		e.Seq = idx
		batch = append(batch, e)
		size += len(line)
		if len(batch) >= maxAuditBatchEvents || size >= maxAuditBatchBytes {
			if err := flush(); err != nil {
				return last, err
			}
		}
		if rerr != nil {
			break
		}
	}
	return last, flush()
}

// drained reports whether every line in the file has been acknowledged.
func drained(t *auditTail) bool {
	fi, err := os.Stat(t.path)
	return err == nil && t.registered && fi.Size() == t.offset
}

var auditFilePattern = regexp.MustCompile(`^(\d+)\.([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// sweep finishes the streams that crashed or killed processes left behind: a
// file named for a pid that no longer runs is uploaded from the server's cursor
// and removed once the server holds all of it. A file whose process is alive is
// that process's own and is never touched.
func (s *AuditSyncer) sweep(ctx context.Context, own string, hostOK *bool) error {
	if s.opts.StateDir == "" {
		return nil
	}
	dir := audit.Dir(s.opts.StateDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	done := 0
	for _, de := range entries {
		m := auditFilePattern.FindStringSubmatch(de.Name())
		if m == nil || m[2] == own || de.IsDir() {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		if proc.Alive(pid) {
			continue
		}
		if done >= maxSweepFiles {
			return nil
		}
		done++
		path := filepath.Join(dir, de.Name())
		if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
			_ = os.Remove(path) // a process that never recorded anything
			continue
		}
		t := &auditTail{path: path, id: m[2], serverLast: -1}
		if err := s.step(ctx, t, hostOK, false); err != nil {
			return err
		}
		if drained(t) {
			_ = os.Remove(path)
		}
	}
	return nil
}
