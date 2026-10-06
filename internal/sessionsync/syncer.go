package sessionsync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Upload limits. The server takes at most 200 entries and 8 MiB per batch;
// staying well under both leaves room for JSON overhead.
const (
	maxBatchEntries = 100
	maxBatchBytes   = 2 << 20
	// maxLineBytes caps one uploaded line. Belai already caps tool results at
	// 32 KiB, so only a pathological line is cut, and it is cut visibly.
	maxLineBytes = 1 << 20
)

// Options configures a Syncer. Zero intervals take the defaults.
type Options struct {
	Client        *Client
	HostID        string
	Host          Host
	RemotePrompts bool
	// RemoteAnswers delivers web answers to the host's open asks. A session
	// whose ask gate the web can switch changes it with SetRemoteAnswers.
	RemoteAnswers bool
	// RemoteCommands delivers session controls from the web (belai rc
	// --web-controls). Off, every command is refused.
	RemoteCommands bool
	// RemoteShell delivers shell lines from the web (belai rc --web-shell).
	// Off, every shell line is refused.
	RemoteShell bool
	// OnControls is called, on the syncer's goroutine, with the website's
	// pending controls for the live session each time the server returns some
	// (registration and every heartbeat). It must not block. nil ignores them.
	OnControls func(sessionID string, c Controls)
	// Git returns the live session's repository reading (internal/gitsync.Info
	// as JSON), or nil when there is none. It is polled on every tick, so it
	// must return a cached value and never block. nil sends no git state.
	Git func() json.RawMessage

	TickEvery      time.Duration // how often the file is re-read without a nudge (2s)
	HeartbeatEvery time.Duration // liveness beat (15s; the server's window is 45s)
	InboxWait      time.Duration // long-poll wait (20s, under the 30s header timeout)
}

// SessionInfo identifies the session to mirror and what the TUI knows about
// it. Fields the JSONL later reveals (name, cwd, model, mode) are picked up
// from the file as it is tailed.
type SessionInfo struct {
	ID          string
	Path        string // the session's .jsonl
	ProjectKey  string
	ProjectName string
	Cwd         string
	Name        string
	Model       string
	Provider    string
	Mode        string
	// ActiveProfile: the agent profile the session ran under, read from the
	// latest session_meta line so a ctrl+p switch reaches the website.
	ActiveProfile   string
	ParentSessionID string
	ResumedFromID   string
	// DispatchID: the rc request that started this session (see Dispatch).
	DispatchID string
	// Git is the session's repository (internal/gitsync.Info as JSON), kept
	// current from Options.Git. Empty outside a repository.
	Git json.RawMessage
}

// Status is a snapshot for /sync status.
type Status struct {
	SessionID  string
	Registered bool
	LastSeq    int64 // highest line the server holds
	LastError  string
	LastOK     time.Time
}

type ack struct {
	id, status, reason, entryID string
	answer                      bool // a web answer's ack, not a prompt's
}

// nudgeCoalesce is how long the mirror waits after a nudge for the lines that
// usually follow it (an ask and its notice, a tool start and its row), so a
// burst goes up as one batch instead of one request per line.
const nudgeCoalesce = 30 * time.Millisecond

type metaUpdate struct {
	id                    string
	model, provider, mode string
}

// Syncer mirrors the active session and, when RemotePrompts is on, delivers
// web prompts on Prompts(). All network I/O runs on its own goroutines; every
// method called from the TUI is non-blocking.
type Syncer struct {
	opts Options

	activate chan SessionInfo
	meta     chan metaUpdate
	nudge    chan struct{}
	acks     chan ack
	closing  chan time.Duration
	stopped  chan struct{}
	prompts  chan RemotePrompt
	answers  chan RemoteAnswer
	drafts   chan RemoteDraft
	commands chan RemoteCommand

	cancel    context.CancelFunc
	closeOnce sync.Once

	mu     sync.Mutex
	status Status
	live   string // the registered session id, for the inbox loop
	// remoteAnswers, ctlState and ctlGen are the parts of the registration
	// the host changes while the session runs; a new ctlGen re-registers.
	remoteAnswers bool
	ctlState      json.RawMessage
	ctlGen        int
}

// New builds a Syncer. Call Start to run it.
func New(opts Options) *Syncer {
	if opts.TickEvery <= 0 {
		opts.TickEvery = 2 * time.Second
	}
	if opts.HeartbeatEvery <= 0 {
		opts.HeartbeatEvery = 15 * time.Second
	}
	if opts.InboxWait <= 0 {
		opts.InboxWait = 20 * time.Second
	}
	return &Syncer{
		opts:     opts,
		activate: make(chan SessionInfo, 4),
		meta:     make(chan metaUpdate, 8),
		nudge:    make(chan struct{}, 1),
		acks:     make(chan ack, 32),
		closing:  make(chan time.Duration, 1),
		stopped:  make(chan struct{}),
		prompts:  make(chan RemotePrompt, 16),
		answers:  make(chan RemoteAnswer, 16),
		drafts:   make(chan RemoteDraft, 4),
		commands: make(chan RemoteCommand, 16),

		remoteAnswers: opts.RemoteAnswers,
	}
}

// Start runs the mirror loop and, if enabled, the inbox loop.
func (s *Syncer) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
	go s.vaultLease(ctx)
	if s.opts.RemotePrompts || s.opts.RemoteAnswers || s.opts.RemoteCommands || s.opts.RemoteShell {
		go s.inbox(ctx)
	}
}

// Activate switches the mirror to a session. The previous one, if it was
// registered, is ended so the website moves it to History.
func (s *Syncer) Activate(info SessionInfo) {
	select {
	case s.activate <- info:
	default:
		// Four switches queued faster than the loop drains them: keep the
		// newest by draining one and retrying once.
		select {
		case <-s.activate:
		default:
		}
		select {
		case s.activate <- info:
		default:
		}
	}
}

// UpdateMeta records a model/provider/mode change the file may not carry yet.
func (s *Syncer) UpdateMeta(sessionID, model, provider, mode string) {
	select {
	case s.meta <- metaUpdate{id: sessionID, model: model, provider: provider, mode: mode}:
	default:
	}
}

// pollGit takes the host's current git reading for the live session; a change
// is sent with the next registration refresh.
func (s *Syncer) pollGit(t *tail) {
	if s.opts.Git == nil || t == nil {
		return
	}
	if g := s.opts.Git(); len(g) > 0 && string(g) != string(t.info.Git) {
		t.info.Git = g
		t.dirty = true
	}
}

// Nudge asks for an immediate re-read after the TUI appended a line.
func (s *Syncer) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Prompts delivers web prompts claimed from the inbox.
func (s *Syncer) Prompts() <-chan RemotePrompt { return s.prompts }

// RemotePromptsEnabled reports whether web prompts are taken.
func (s *Syncer) RemotePromptsEnabled() bool { return s.opts.RemotePrompts }

// Answers delivers web answers to open asks claimed from the inbox.
func (s *Syncer) Answers() <-chan RemoteAnswer { return s.answers }

// RemoteAnswersEnabled reports whether web answers are taken.
func (s *Syncer) RemoteAnswersEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteAnswers
}

// Commands delivers session controls claimed from the inbox.
func (s *Syncer) Commands() <-chan RemoteCommand { return s.commands }

// SetSessionControls records the session's current controls and whether it
// takes web answers (ask on), and re-registers the session so the website
// shows both. The inbox must already be running: a syncer that may turn
// answers on is built with RemoteCommands.
func (s *Syncer) SetSessionControls(state json.RawMessage, remoteAnswers bool) {
	s.mu.Lock()
	s.ctlState = append(json.RawMessage(nil), state...)
	s.remoteAnswers = remoteAnswers
	s.ctlGen++
	s.mu.Unlock()
	s.Nudge()
}

// AckCommand reports a session control's outcome in the background.
func (s *Syncer) AckCommand(commandID, status, reason string, state json.RawMessage) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := s.opts.Client.AckCommand(ctx, commandID, status, reason, state); err != nil {
			s.setErr(fmt.Errorf("ack command: %w", err))
		}
	}()
}

// pollControls marks the session for re-registration after the controls
// changed.
func (s *Syncer) pollControls(t *tail) {
	if t == nil {
		return
	}
	s.mu.Lock()
	gen := s.ctlGen
	s.mu.Unlock()
	if gen != t.ctlGen {
		t.ctlGen = gen
		t.dirty = true
	}
}

// AckAnswer reports a web answer's outcome, like Ack: an accepted answer
// refers to the ask_answer line just written, so it waits for that upload.
func (s *Syncer) AckAnswer(answerID, status, reason, entryID string) {
	a := ack{id: answerID, status: status, reason: reason, entryID: entryID, answer: true}
	if status == AckAccepted {
		select {
		case s.acks <- a:
			s.Nudge()
			return
		default:
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := s.sendAck(ctx, a); err != nil {
			s.setErr(fmt.Errorf("ack: %w", err))
		}
	}()
}

// Drafts delivers agent-draft requests claimed from the inbox.
func (s *Syncer) Drafts() <-chan RemoteDraft { return s.drafts }

// DraftResult posts an agent draft's outcome in the background: done with the
// result, or refused with a reason. The server refuses a result for a draft
// the user cancelled or that expired; that refusal is not an error here.
func (s *Syncer) DraftResult(draftID, status, reason string, result any) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := s.opts.Client.DraftResult(ctx, draftID, status, reason, result); err != nil && !IsConflict(err) {
			s.setErr(fmt.Errorf("draft result: %w", err))
		}
	}()
}

func (s *Syncer) sendAck(ctx context.Context, a ack) error {
	if a.answer {
		return s.opts.Client.AckAnswer(ctx, a.id, a.status, a.reason, a.entryID)
	}
	return s.opts.Client.Ack(ctx, a.id, a.status, a.reason, a.entryID)
}

// Ack reports a prompt's outcome in the background. An accepted ack refers
// to a user line the host just wrote, so it goes through the mirror loop and
// is sent only after that line has been uploaded: the website never reads
// "accepted" for a line it does not have yet.
func (s *Syncer) Ack(promptID, status, reason, entryID string) {
	if status == AckAccepted {
		select {
		case s.acks <- ack{id: promptID, status: status, reason: reason, entryID: entryID}:
			s.Nudge()
			return
		default:
			// A full queue falls through to the direct send: late order beats
			// a lost ack.
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := s.opts.Client.Ack(ctx, promptID, status, reason, entryID); err != nil {
			s.setErr(fmt.Errorf("ack: %w", err))
		}
	}()
}

// Status returns a snapshot.
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Close flushes what the file holds, ends the live session and stops, waiting
// at most timeout.
func (s *Syncer) Close(timeout time.Duration) {
	s.closeOnce.Do(func() {
		s.closing <- timeout
		select {
		case <-s.stopped:
		case <-time.After(timeout + 500*time.Millisecond):
		}
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *Syncer) setErr(err error) {
	s.mu.Lock()
	s.status.LastError = err.Error()
	s.mu.Unlock()
}

// tail is the mirror state for one session file. offset/seq point at the first
// line not yet known to be on the server.
type tail struct {
	info       SessionInfo
	registered bool
	serverLast int64
	offset     int64
	seq        int64
	dirty      bool
	lastBeat   time.Time
	ctlGen     int
}

func (s *Syncer) run(ctx context.Context) {
	defer close(s.stopped)
	var cur *tail
	hostOK := false
	var backoff time.Duration
	var retryAt time.Time
	var pendingAcks []ack
	tick := time.NewTicker(s.opts.TickEvery)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case timeout := <-s.closing:
			fctx, cancel := context.WithTimeout(context.Background(), timeout)
			// Whatever was queued ahead of Close is taken first. select picks
			// among ready cases at random, so a session that was activated and
			// nudged just before Close could otherwise still be unregistered
			// here, and a short session would never reach the server.
			cur = s.takeQueued(fctx, cur, &pendingAcks)
			if cur != nil {
				// step registers the session if it is not yet, then uploads.
				if s.step(fctx, cur, &hostOK, false) == nil {
					s.sendAcks(fctx, &pendingAcks)
				}
				if cur.registered {
					_ = s.opts.Client.End(fctx, cur.info.ID)
				}
			}
			cancel()
			return
		case info := <-s.activate:
			if cur != nil && cur.info.ID == info.ID {
				// Same session re-announced (e.g. after resume): refresh meta.
				cur.info = mergeInfo(cur.info, info)
				cur.dirty = true
				break
			}
			if cur != nil && cur.registered {
				prev := cur
				go func() {
					fctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
					defer cancel()
					_ = s.upload(fctx, prev)
					_ = s.opts.Client.End(fctx, prev.info.ID)
				}()
			}
			cur = &tail{info: info, serverLast: -1}
			s.setLive("", Status{SessionID: info.ID, LastSeq: -1})
			retryAt = time.Time{}
		case m := <-s.meta:
			if cur != nil && cur.info.ID == m.id {
				if m.model != "" && m.model != cur.info.Model ||
					m.provider != "" && m.provider != cur.info.Provider ||
					m.mode != "" && m.mode != cur.info.Mode {
					cur.info.Model, cur.info.Provider, cur.info.Mode = orStr(m.model, cur.info.Model),
						orStr(m.provider, cur.info.Provider), orStr(m.mode, cur.info.Mode)
					cur.dirty = true
				}
			}
		case a := <-s.acks:
			pendingAcks = append(pendingAcks, a)
		case <-s.nudge:
			s.coalesce(ctx)
		case <-tick.C:
		}
		s.pollGit(cur)
		s.pollControls(cur)
		if cur == nil {
			s.sendAcks(ctx, &pendingAcks)
			continue
		}
		if time.Now().Before(retryAt) {
			continue
		}
		if err := s.step(ctx, cur, &hostOK, true); err != nil {
			if errors.Is(err, ErrNotFound) {
				// The server no longer knows the session (or the host):
				// register again from scratch on the next step.
				cur.registered = false
				hostOK = false
			}
			backoff = min(max(2*backoff, 2*time.Second), time.Minute)
			retryAt = time.Now().Add(backoff)
			s.setErr(err)
			continue
		}
		backoff = 0
		// Everything the file held is on the server now, including the user
		// line an accepted ack points at.
		s.sendAcks(ctx, &pendingAcks)
	}
}

// takeQueued applies the activations, metadata updates and acks already queued
// when Close arrives, and returns the session to flush. A session it replaces
// is uploaded and ended on the way, as a live switch would.
func (s *Syncer) takeQueued(ctx context.Context, cur *tail, pending *[]ack) *tail {
	for {
		select {
		case info := <-s.activate:
			if cur != nil && cur.info.ID == info.ID {
				cur.info = mergeInfo(cur.info, info)
				cur.dirty = true
				continue
			}
			if cur != nil && cur.registered {
				_ = s.upload(ctx, cur)
				_ = s.opts.Client.End(ctx, cur.info.ID)
			}
			cur = &tail{info: info, serverLast: -1}
		case m := <-s.meta:
			if cur != nil && cur.info.ID == m.id {
				cur.info.Model, cur.info.Provider, cur.info.Mode = orStr(m.model, cur.info.Model),
					orStr(m.provider, cur.info.Provider), orStr(m.mode, cur.info.Mode)
				cur.dirty = true
			}
		case a := <-s.acks:
			*pending = append(*pending, a)
		default:
			return cur
		}
	}
}

// sendAcks sends the queued accepted acks, keeping any that fail for the next
// round.
func (s *Syncer) sendAcks(ctx context.Context, pending *[]ack) {
	kept := (*pending)[:0]
	for _, a := range *pending {
		if err := s.sendAck(ctx, a); err != nil && !errors.Is(err, ErrNotFound) {
			s.setErr(fmt.Errorf("ack: %w", err))
			kept = append(kept, a)
		}
	}
	*pending = kept
}

func (s *Syncer) step(ctx context.Context, t *tail, hostOK *bool, live bool) error {
	if _, err := os.Stat(t.info.Path); err != nil {
		// No file yet: the TUI creates the live session's file at startup,
		// but another caller may point at a session that never gets one, and
		// such a session is never registered.
		return nil
	}
	if !*hostOK {
		if err := s.opts.Client.PutHost(ctx, s.opts.HostID, s.opts.Host); err != nil {
			return err
		}
		*hostOK = true
	}
	if !t.registered {
		// Read the file once first so name/cwd/model are known at
		// registration; nothing is uploaded until the server's mark is known.
		s.scanMeta(t)
		last, ctl, err := s.opts.Client.PutSessionControls(ctx, t.info.ID, s.metaFor(t))
		if err != nil {
			return err
		}
		t.registered, t.serverLast, t.offset, t.seq, t.dirty = true, last, 0, 0, false
		t.lastBeat = time.Now()
		if live {
			s.applyControls(t.info.ID, ctl)
		}
		if live {
			s.setLive(t.info.ID, Status{SessionID: t.info.ID, Registered: true, LastSeq: last})
		}
	}
	if err := s.upload(ctx, t); err != nil {
		return err
	}
	if t.dirty {
		_, ctl, err := s.opts.Client.PutSessionControls(ctx, t.info.ID, s.metaFor(t))
		if err != nil {
			return err
		}
		t.dirty = false
		t.lastBeat = time.Now()
		if live {
			s.applyControls(t.info.ID, ctl)
		}
	}
	if time.Since(t.lastBeat) >= s.opts.HeartbeatEvery {
		ctl, err := s.opts.Client.HeartbeatControls(ctx, t.info.ID)
		if err != nil {
			return err
		}
		t.lastBeat = time.Now()
		if live {
			s.applyControls(t.info.ID, ctl)
		}
	}
	if live {
		s.mu.Lock()
		s.status.LastError = ""
		s.status.LastOK = time.Now()
		s.status.LastSeq = t.serverLast
		s.mu.Unlock()
	}
	return nil
}

func (s *Syncer) setLive(id string, st Status) {
	s.mu.Lock()
	s.live = id
	st.LastOK = s.status.LastOK
	s.status = st
	s.mu.Unlock()
}

func (s *Syncer) liveSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *Syncer) metaFor(t *tail) SessionMeta {
	i := t.info
	return SessionMeta{
		HostID: s.opts.HostID, ProjectKey: i.ProjectKey, ProjectName: i.ProjectName, Cwd: i.Cwd,
		Name: i.Name, Model: i.Model, Provider: i.Provider, Mode: i.Mode, ActiveProfile: i.ActiveProfile,
		ParentSessionID: i.ParentSessionID, ResumedFromID: i.ResumedFromID,
		RemotePrompts: s.opts.RemotePrompts, RemoteAnswers: s.RemoteAnswersEnabled(),
		DispatchID: i.DispatchID, Git: i.Git,
		Controls: s.opts.RemoteCommands, ControlState: s.controlState(),
		Shell: s.opts.RemoteShell,
	}
}

func (s *Syncer) controlState() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctlState
}

// applyControls hands the website's pending controls for the live session to
// the host.
func (s *Syncer) applyControls(id string, c Controls) {
	if s.opts.OnControls != nil && c.GitSync != nil {
		s.opts.OnControls(id, c)
	}
}

// scanMeta reads the whole file for display metadata only.
func (s *Syncer) scanMeta(t *tail) {
	f, err := os.Open(t.info.Path)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var seq int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return
		}
		t.observe(parseLine(line, seq))
		seq++
		if err != nil {
			return
		}
	}
}

// upload sends every complete line after the server's mark, in order. The
// offset advances only past lines the server has acknowledged, so a failure
// re-reads and re-sends — harmless, because the server ignores repeats.
func (s *Syncer) upload(ctx context.Context, t *tail) error {
	if !t.registered {
		return nil
	}
	f, err := os.Open(t.info.Path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	pos, seq := t.offset, t.seq
	var batch []Entry
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		last, err := s.opts.Client.PostEntries(ctx, t.info.ID, batch)
		if err != nil {
			return err
		}
		t.offset, t.seq = pos, seq
		if last > t.serverLast {
			t.serverLast = last
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
		e := parseLine(line, idx)
		t.observe(e)
		batch = append(batch, e)
		size += len(line)
		if len(batch) >= maxBatchEntries || size >= maxBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		if rerr != nil {
			break
		}
	}
	return flush()
}

// parseLine turns one JSONL line into an upload entry. A line that is not a
// valid entry still takes its seq, so the website's sequence has no hole.
func parseLine(line []byte, seq int64) Entry {
	if len(line) > maxLineBytes {
		return Entry{Seq: seq, ID: fmt.Sprintf("oversize-%d", seq), Type: "system",
			Content: fmt.Sprintf("[line of %d bytes not synced]", len(line))}
	}
	var e Entry
	if err := json.Unmarshal(line, &e); err != nil || e.Type == "" {
		return Entry{Seq: seq, ID: fmt.Sprintf("invalid-%d", seq), Type: "invalid"}
	}
	e.Seq = seq
	if e.ID == "" || len(e.ID) > 64 {
		e.ID = fmt.Sprintf("line-%d", seq)
	}
	if len(e.Meta) > 0 && !json.Valid(e.Meta) {
		e.Meta = nil
	}
	return e
}

// observe folds display metadata the file carries into the session info.
func (t *tail) observe(e Entry) {
	switch e.Type {
	case "session_name":
		t.set(&t.info.Name, strings.TrimSpace(e.Content))
	case "session_meta":
		var m struct {
			Cwd           string `json:"cwd"`
			Mode          string `json:"mode"`
			ResumedFrom   string `json:"resumedFrom"`
			ActiveProfile string `json:"activeProfile"`
		}
		if json.Unmarshal([]byte(e.Content), &m) == nil {
			t.set(&t.info.Cwd, m.Cwd)
			t.set(&t.info.Mode, m.Mode)
			t.set(&t.info.ActiveProfile, m.ActiveProfile)
			t.set(&t.info.ResumedFromID, m.ResumedFrom)
		}
	case "assistant":
		var m struct {
			Model    string `json:"model"`
			Provider string `json:"provider"`
			Mode     string `json:"mode"`
		}
		if json.Unmarshal(e.Meta, &m) == nil {
			t.set(&t.info.Model, m.Model)
			t.set(&t.info.Provider, m.Provider)
			t.set(&t.info.Mode, m.Mode)
		}
	}
}

func (t *tail) set(field *string, v string) {
	if v != "" && *field != v {
		*field = v
		t.dirty = true
	}
}

func mergeInfo(old, n SessionInfo) SessionInfo {
	n.Name = orStr(n.Name, old.Name)
	n.Cwd = orStr(n.Cwd, old.Cwd)
	n.Model = orStr(n.Model, old.Model)
	n.Provider = orStr(n.Provider, old.Provider)
	n.Mode = orStr(n.Mode, old.Mode)
	n.ActiveProfile = orStr(n.ActiveProfile, old.ActiveProfile)
	n.ParentSessionID = orStr(n.ParentSessionID, old.ParentSessionID)
	n.ResumedFromID = orStr(n.ResumedFromID, old.ResumedFromID)
	if len(n.Git) == 0 {
		n.Git = old.Git
	}
	return n
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Backfill uploads finished sessions straight into History: each is
// registered, mirrored in full from its file and ended. Sessions the server
// already holds upload only what it is missing. It runs on the caller's
// goroutine and returns how many sessions completed.
func (s *Syncer) Backfill(ctx context.Context, infos []SessionInfo) (int, error) {
	hostOK := false
	done := 0
	for _, info := range infos {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		t := &tail{info: info, serverLast: -1}
		if err := s.step(ctx, t, &hostOK, false); err != nil {
			return done, fmt.Errorf("%s: %w", info.ID, err)
		}
		if !t.registered {
			continue // no file, nothing to upload
		}
		if err := s.opts.Client.End(ctx, info.ID); err != nil {
			return done, fmt.Errorf("%s: %w", info.ID, err)
		}
		done++
	}
	return done, nil
}

// coalesce holds a nudged upload briefly so the lines written right after it
// ride the same batch. Nudges arriving meanwhile are folded in.
func (s *Syncer) coalesce(ctx context.Context) {
	t := time.NewTimer(nudgeCoalesce)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.nudge:
		case <-t.C:
			return
		}
	}
}

// inbox long-polls for web prompts and answers while a session is registered.
// It asks only for that session's requests: another Belai on the same host
// (a second TUI, an rc session) shares the host id and polls for its own.
func (s *Syncer) inbox(ctx context.Context) {
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		live := s.liveSession()
		if live == "" {
			if !sleep(ctx, s.opts.TickEvery) {
				return
			}
			continue
		}
		batch, err := s.opts.Client.InboxBatch(ctx, s.opts.HostID, live, s.opts.InboxWait)
		prompts, answers, drafts := batch.Prompts, batch.Answers, batch.Drafts
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(max(2*backoff, 2*time.Second), time.Minute)
			if !sleep(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		// Answers first: the host is blocked on an ask until one arrives.
		for _, a := range answers {
			if !s.RemoteAnswersEnabled() {
				s.AckAnswer(a.ID, AckRefused, "this host does not take answers from the web", "")
				continue
			}
			select {
			case s.answers <- a:
			case <-ctx.Done():
				return
			}
		}
		// Controls next: they apply to the turn the prompts start.
		for _, c := range batch.Commands {
			if c.Shell != "" {
				if !s.opts.RemoteShell {
					s.AckCommand(c.ID, AckRefused, "this host does not run shell lines from the web", nil)
					continue
				}
			} else if !s.opts.RemoteCommands {
				s.AckCommand(c.ID, AckRefused, "this host does not take session controls from the web", nil)
				continue
			}
			select {
			case s.commands <- c:
			case <-ctx.Done():
				return
			}
		}
		for _, p := range prompts {
			select {
			case s.prompts <- p:
			case <-ctx.Done():
				return
			}
		}
		// Agent drafts ride the web-prompt switch: a premise is web-typed text.
		for _, d := range drafts {
			if !s.opts.RemotePrompts {
				s.DraftResult(d.ID, DraftRefused, "this host does not take prompts from the web", nil)
				continue
			}
			select {
			case s.drafts <- d:
			case <-ctx.Done():
				return
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
