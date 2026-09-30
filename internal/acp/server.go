// Package acp serves the Agent Client Protocol over stdio, so editors that
// speak ACP (Zed, JetBrains IDEs, Neovim plugins) can drive Belai as their
// agent. Each ACP session is an agent.Session built exactly as the headless
// CLI builds one, so the classifier, posture gates, permission rules and
// budgets all apply; permission asks become session/request_permission
// requests to the editor.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/version"
)

// ProtocolVersion is the ACP major version Belai speaks.
const ProtocolVersion = 1

// Builder builds the agent session for a working directory. It must refuse
// a directory the user has not trusted.
type Builder func(ctx context.Context, cwd, sessionID string) (*agent.Session, error)

// Server is one ACP connection.
type Server struct {
	build Builder
	conn  *jsonrpc.Conn
	// ready is closed once conn is set; the read loop may dispatch a
	// request before NewConn has returned.
	ready chan struct{}

	mu       sync.Mutex
	sessions map[string]*acpSession
	opts     Options
	// recording is set once a session owns the process-wide role-manager
	// record sink; a connection normally carries one session.
	recording bool
	// client is the editor's declared name (initialize clientInfo), cleaned.
	// It prefixes every session name so the record says which editor drove it.
	client string
}

// Mirror follows the sessions of a connection so their transcripts can be
// mirrored elsewhere (the Vulnetix web session sync). It sees only a session's
// id, directory and harness-composed name; the transcript lines are read from
// the file the session log already wrote.
type Mirror interface {
	// Opened reports a new session and its display name.
	Opened(id, cwd, name string)
	// Touched reports that the session's transcript grew.
	Touched(id string)
	// Close ends every mirrored session; called once when the connection ends.
	Close()
}

// Options are the optional parts of a connection.
type Options struct {
	// Transcript opens the session transcript for a new editor session. Nil
	// keeps none. The first session on a connection also receives every
	// role-manager decision made in the process, shown or not; the sink is
	// process-wide, so a connection carrying several sessions attributes the
	// decisions to the first.
	Transcript func(cwd, sessionID string) *turnlog.Log
	// PostEnd runs the post-end test pass after a prompt turn that completed
	// a goal. fix runs a fail-branch turn on the session and streams its
	// events, permission asks included, to the editor; notify sends one
	// harness line to the editor. It returns false when the user's settings
	// do not run the pass. Nil never runs one. There is no session-end moment
	// an editor can watch, so a completed goal is the only trigger over ACP.
	PostEnd func(ctx context.Context, cwd string, sess *agent.Session, fix testpass.Fixer, notify func(string)) (testpass.Outcome, bool)
	// Mirror follows each session's transcript. Nil mirrors nothing, and a
	// session without a transcript is never reported to it. Nothing arrives
	// from the far side: the editor owns the conversation.
	Mirror Mirror
}

type acpSession struct {
	id      string
	cwd     string
	agent   *agent.Session
	mu      sync.Mutex
	history []run.Turn
	cancel  context.CancelFunc
	// log is the session transcript (a no-op Log when none is kept); detach
	// stops its role-manager record sink.
	log    *turnlog.Log
	detach func()
	// named is set once the first prompt has given the session its name.
	named bool
	// prog is the running prompt's progress state; lastSent is when an update
	// last went to the editor (unix nanoseconds), for the heartbeat.
	prog     *progress
	lastSent atomic.Int64
	// updated is when the session was opened or last finished a prompt.
	updated time.Time
	// mode is the editor's chosen mode id (modeAuto until it picks one).
	mode string
	// headed is set once the session's header has gone to the editor.
	headed bool
	// always holds tool names the editor allowed for the rest of the
	// session (allow_always). It never reaches a settings file.
	always map[string]bool
	// snaps keeps the history on either side of each finished turn, keyed by
	// the turn's user entry in the transcript, so /tree can return to a point
	// without rebuilding it from the transcript (which holds the raw prompt,
	// not the sanitized one the model was given).
	snaps map[string]turnSnap
}

// Serve runs the protocol on r and w until the peer disconnects.
func Serve(ctx context.Context, r io.Reader, w io.Writer, build Builder) error {
	return ServeWith(ctx, r, w, build, Options{})
}

// ServeWith is Serve with options.
func ServeWith(ctx context.Context, r io.Reader, w io.Writer, build Builder, opts Options) error {
	s := &Server{build: build, sessions: map[string]*acpSession{}, ready: make(chan struct{}), opts: opts}
	s.conn = jsonrpc.NewConn(r, w, s.handle)
	close(s.ready)
	select {
	case <-s.conn.Done():
	case <-ctx.Done():
		s.conn.Close()
	}
	s.mu.Lock()
	for _, ss := range s.sessions {
		ss.mu.Lock()
		if ss.cancel != nil {
			ss.cancel()
		}
		ss.mu.Unlock()
		if ss.detach != nil {
			ss.detach()
		}
	}
	s.mu.Unlock()
	if s.opts.Mirror != nil {
		s.opts.Mirror.Close()
	}
	if err := s.conn.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Methods lists every ACP method the server answers.
var Methods = []string{"initialize", "authenticate", "session/new", "session/prompt", "session/cancel", "session/list", "session/close", "session/set_mode"}

func (s *Server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	<-s.ready
	switch method {
	case "initialize":
		return s.initialize(params)
	case "authenticate":
		return map[string]any{}, nil
	case "session/new":
		return s.newSession(ctx, params)
	case "session/prompt":
		return s.prompt(ctx, params)
	case "session/cancel":
		s.cancelSession(params)
		return nil, nil
	case "session/list":
		return s.listSessions(params)
	case "session/close":
		return s.closeSession(params)
	case "session/set_mode":
		return s.setMode(params)
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "belai does not support %s", method)
}

func (s *Server) initialize(params json.RawMessage) (any, error) {
	name := clientName(params)
	s.mu.Lock()
	s.client = name
	s.mu.Unlock()
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession":         false,
			"sessionCapabilities": map[string]any{"list": map[string]any{}, "close": map[string]any{}},
			"promptCapabilities":  map[string]any{"image": true, "audio": false, "embeddedContext": true},
			"mcpCapabilities":     map[string]any{"http": false, "sse": false},
		},
		"agentInfo":   map[string]any{"name": "belai", "title": "Vulnetix Belai", "version": version.Version},
		"authMethods": []any{},
	}, nil
}

func (s *Server) newSession(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
	}
	if !filepath.IsAbs(p.Cwd) {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cwd must be an absolute path")
	}
	ss, err := s.openSession(ctx, filepath.Clean(p.Cwd))
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	// The editor learns the session's slash commands only after it has the id.
	return &jsonrpc.Reply{
		Result: map[string]any{"sessionId": ss.id, "modes": modeState(modeAuto)},
		After:  func() { s.announceCommands(ss) },
	}, nil
}

// openSession builds an agent and a transcript for a new editor session and
// registers it with the server and the mirror.
func (s *Server) openSession(ctx context.Context, cwd string) (*acpSession, error) {
	id := session.MustID()
	ag, err := s.build(ctx, cwd, id)
	if err != nil {
		return nil, err
	}
	ss := &acpSession{id: id, cwd: cwd, agent: ag, always: map[string]bool{}, log: turnlog.New(nil), updated: time.Now(), mode: modeAuto, snaps: map[string]turnSnap{}}
	if s.opts.Transcript != nil {
		if l := s.opts.Transcript(cwd, id); l != nil {
			ss.log = l
		}
	}
	s.mu.Lock()
	if !s.recording {
		s.recording = true
		ss.detach = ss.log.AttachRoleManager()
	}
	s.sessions[id] = ss
	client := s.client
	s.mu.Unlock()
	if ss.log.Writer() != nil {
		name := sessionName(client, dirName(ss.cwd))
		ss.log.Name(name)
		if s.opts.Mirror != nil {
			s.opts.Mirror.Opened(id, ss.cwd, name)
		}
	}
	return ss, nil
}

func (s *Server) lookup(id string) *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) cancelSession(params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &p)
	if ss := s.lookup(p.SessionID); ss != nil {
		ss.mu.Lock()
		if ss.cancel != nil {
			ss.cancel()
		}
		ss.mu.Unlock()
	}
}

// contentBlock is the subset of ACP content Belai reads from a prompt.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	URI  string `json:"uri,omitempty"`
	Name string `json:"name,omitempty"`
	// Data and MimeType carry an image block: base64 bytes and the media type the
	// editor declares (which Belai does not trust; the decoder decides).
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Resource *struct {
		URI  string `json:"uri"`
		Text string `json:"text,omitempty"`
	} `json:"resource,omitempty"`
}

// promptText flattens the editor's content blocks into one prompt. Embedded
// file text is marked as attached context; everything reaches the agent
// through its normal admission and classification.
func promptText(blocks []contentBlock) string {
	var b strings.Builder
	for _, c := range blocks {
		switch c.Type {
		case "text":
			b.WriteString(c.Text)
		case "resource_link":
			fmt.Fprintf(&b, " (file: %s)", c.URI)
		case "resource":
			if c.Resource != nil {
				fmt.Fprintf(&b, "\n\nAttached from the editor, %s:\n%s\n", c.Resource.URI, c.Resource.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) prompt(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string         `json:"sessionId"`
		Prompt    []contentBlock `json:"prompt"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
	}
	ss := s.lookup(p.SessionID)
	if ss == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown session %q", p.SessionID)
	}
	text := promptText(p.Prompt)
	imgs, markers, notes := promptImages(p.Prompt)
	if text == "" && len(imgs) > 0 {
		text = imagePlaceholder
	}
	if text == "" {
		if len(notes) > 0 {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "no image could be admitted: %s", strings.Join(notes, "; "))
		}
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "empty prompt")
	}

	// /tree and /fork are the harness's own commands: the model never sees them.
	if reply, ok := s.sessionCommand(ctx, ss, text); ok {
		return reply, nil
	}

	ss.mu.Lock()
	if ss.cancel != nil {
		ss.mu.Unlock()
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "a prompt is already running in this session")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	ss.cancel = cancel
	history := append([]run.Turn(nil), ss.history...)
	ss.mu.Unlock()
	defer func() {
		cancel()
		ss.mu.Lock()
		ss.cancel = nil
		ss.mu.Unlock()
	}()

	var userMeta map[string]any
	if len(markers) > 0 {
		// A marker per image and never the bytes, as in the TUI transcript.
		userMeta = map[string]any{"images": markers}
	}
	userID := ss.log.User(text, userMeta)
	s.nameFromPrompt(ss, text)
	s.touch(ss)
	var res run.Result
	var runErr error
	s.sendHeader(ss)
	ss.prog = newProgress()
	started := time.Now()
	ss.lastSent.Store(started.UnixNano())
	ss.mu.Lock()
	force := forcedMode(ss.mode)
	ss.mu.Unlock()
	events := ss.agent.RunStream(turnCtx, history, agent.TurnInput{Prompt: text, Attachments: imgs, Directive: imageNotesDirective(notes), ForceMode: force})
	beat := time.NewTicker(heartbeatEvery / 2)
	defer beat.Stop()
run:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break run
			}
			ss.log.Observe(ev)
			if ev.Kind == agent.EventToolResultKind {
				s.touch(ss)
			}
			switch ev.Kind {
			case agent.EventDoneKind:
				res = ev.Result
			case agent.EventErrorKind:
				runErr = ev.Err
			default:
				s.forward(turnCtx, ss, ev)
			}
		case <-beat.C:
			// A slow provider call or classifier pass sends nothing for a
			// while; say so, so the panel never looks dead.
			if time.Since(time.Unix(0, ss.lastSent.Load())) >= heartbeatEvery {
				s.note(ss, "Still working (%s)", time.Since(started).Round(time.Second))
			}
		}
	}
	ss.log.Flush()
	s.touch(ss)
	if turnCtx.Err() != nil {
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	if runErr != nil {
		var refusal *rolemanager.RefusalError
		if errors.As(runErr, &refusal) {
			return map[string]any{"stopReason": "refusal"}, nil
		}
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: runErr.Error()}
	}
	ss.mu.Lock()
	prompt := res.SanitizedPrompt
	if prompt == "" {
		prompt = text
	}
	n := len(ss.history)
	ss.history = append(ss.history, run.Turn{Role: "user", Content: prompt}, run.Turn{Role: "assistant", Content: res.Reply})
	if userID != "" {
		ss.snaps[userID] = turnSnap{before: ss.history[:n:n], after: ss.history[: n+2 : n+2]}
	}
	ss.updated = time.Now()
	turns := len(ss.history) / 2
	ss.mu.Unlock()
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	s.update(ss, map[string]any{
		"sessionUpdate": "session_info_update",
		"title":         sessionTitle(client, ss.cwd, turns),
		"updatedAt":     ss.updated.UTC().Format(time.RFC3339),
	})
	if res.GoalSentinel == rolemanager.GoalComplete {
		s.postEnd(turnCtx, ss)
	}
	if turnCtx.Err() != nil {
		return map[string]any{"stopReason": "cancelled"}, nil
	}
	return map[string]any{"stopReason": "end_turn"}, nil
}

// sendHeader writes the TUI's header into the chat once per session, at the
// start of its first turn (an update can only follow session/new's answer).
func (s *Server) sendHeader(ss *acpSession) {
	ss.mu.Lock()
	first := !ss.headed
	ss.headed = true
	ss.mu.Unlock()
	if !first {
		return
	}
	var provider, model string
	if ss.agent != nil {
		provider, model = ss.agent.ModelInfo()
	}
	s.update(ss, map[string]any{"sessionUpdate": "agent_message_chunk", "content": textContent(headerText(provider, model))})
}

// nameFromPrompt renames a session after its first prompt: the editor's name,
// then the prompt's first line. The prompt is already admitted; the name is
// cleaned again as a line, so it never carries markup or control runes.
func (s *Server) nameFromPrompt(ss *acpSession, text string) {
	ss.mu.Lock()
	first := !ss.named
	ss.named = true
	ss.mu.Unlock()
	if !first || ss.log.Writer() == nil {
		return
	}
	first1, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	ss.log.Name(sessionName(client, first1))
}

// touch tells the mirror the session's transcript grew.
func (s *Server) touch(ss *acpSession) {
	if s.opts.Mirror != nil && ss.log.Writer() != nil {
		s.opts.Mirror.Touched(ss.id)
	}
}

// postEnd runs the post-end test pass for a session whose goal completed and
// tells the editor how it went. The pass runs under the prompt's own context,
// so an editor cancel stops it, and its fail-branch loop streams through
// forward like any turn, so every permission ask still goes to the editor.
func (s *Server) postEnd(ctx context.Context, ss *acpSession) {
	if s.opts.PostEnd == nil {
		return
	}
	say := func(line string) {
		s.update(ss, map[string]any{"sessionUpdate": "agent_message_chunk", "content": textContent(line + "\n")})
	}
	fix := func(ctx context.Context, req testpass.FixRequest) error {
		var runErr error
		for ev := range ss.agent.RunStream(ctx, nil, testpass.FixInput(req)) {
			ss.log.Observe(ev)
			switch ev.Kind {
			case agent.EventDoneKind:
			case agent.EventErrorKind:
				runErr = ev.Err
			default:
				s.forward(ctx, ss, ev)
			}
		}
		ss.log.Flush()
		return runErr
	}
	out, ran := s.opts.PostEnd(ctx, ss.cwd, ss.agent, fix, say)
	if !ran {
		return
	}
	say(out.Line())
	if out.Report != "" {
		say(out.Report)
	}
}

func (s *Server) update(ss *acpSession, u map[string]any) {
	ss.lastSent.Store(time.Now().UnixNano())
	_ = s.conn.Notify("session/update", map[string]any{"sessionId": ss.id, "update": u})
}

func textContent(t string) map[string]any { return map[string]any{"type": "text", "text": t} }

// forward maps one agent event onto session/update notifications, and a
// permission ask onto a session/request_permission request.
func (s *Server) forward(ctx context.Context, ss *acpSession, ev agent.Event) {
	if s.forwardProgress(ss, ev) {
		return
	}
	switch ev.Kind {
	case agent.EventTextKind:
		if ev.Text != "" {
			s.update(ss, map[string]any{"sessionUpdate": "agent_message_chunk", "content": textContent(ev.Text)})
		}
	case agent.EventReasoningKind:
		if ev.Reasoning != "" {
			s.update(ss, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": textContent(ev.Reasoning)})
		}
	case agent.EventToolStartKind:
		if ev.Tool != nil && ss.prog != nil && ss.prog.wasPending(ev.Tool.ID) {
			// Announced from its first fragment: fill in the arguments.
			s.update(ss, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    ev.Tool.ID,
				"status":        "in_progress",
				"rawInput":      ev.Tool.Args,
			})
		} else if ev.Tool != nil {
			s.update(ss, map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    ev.Tool.ID,
				"title":         ev.Tool.Name,
				"kind":          toolKind(ev.Tool.Name),
				"status":        "in_progress",
				"rawInput":      ev.Tool.Args,
			})
		}
	case agent.EventToolDiffKind:
		if ev.Diff != nil {
			var content []any
			for _, f := range ev.Diff.Files {
				if f.Binary || f.Truncated {
					continue
				}
				d := map[string]any{"type": "diff", "path": f.Path, "newText": f.New}
				if !f.Created {
					d["oldText"] = f.Old
				}
				content = append(content, d)
			}
			if len(content) > 0 {
				s.update(ss, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": ev.ToolCallID, "content": content})
			}
		}
	case agent.EventToolResultKind:
		status := "completed"
		if strings.HasPrefix(ev.ToolResult, "tool result withheld:") || strings.HasPrefix(ev.ToolResult, "tool call rejected:") {
			status = "failed"
		}
		s.update(ss, map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    ev.ToolCallID,
			"status":        status,
			"content":       []any{map[string]any{"type": "content", "content": textContent(ev.ToolResult)}},
		})
	case agent.EventTodosKind:
		if ev.Todos != nil {
			s.update(ss, map[string]any{"sessionUpdate": "plan", "entries": planEntries(ev.Todos)})
		}
	case agent.EventPermissionAskKind:
		if ev.Ask != nil && ev.AskReply != nil {
			ev.AskReply <- agent.PermissionAskReply{Allow: s.askPermission(ctx, ss, ev)}
		}
	case agent.EventClarifyAskKind:
		// The session is built without clarifying questions; answer with no
		// selections so a stray ask can never hang the turn.
		if ev.Reply != nil {
			select {
			case ev.Reply <- clarify.Answers{}:
			case <-ctx.Done():
			}
		}
	}
}

// askPermission asks the editor. allow_always is remembered for this
// session only; any failure or cancellation denies.
func (s *Server) askPermission(ctx context.Context, ss *acpSession, ev agent.Event) bool {
	name := ev.Ask.Name
	ss.mu.Lock()
	always := ss.always[name]
	ss.mu.Unlock()
	if always {
		return true
	}
	title := name
	if ev.Ask.Subject != "" {
		title += " " + ev.Ask.Subject
	}
	params := map[string]any{
		"sessionId": ss.id,
		"toolCall": map[string]any{
			"toolCallId": ev.ToolCallID,
			"title":      title,
			"kind":       toolKind(name),
			"rawInput":   ev.Ask.Args,
		},
		"options": []any{
			map[string]any{"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
			map[string]any{"optionId": "allow_always", "name": "Allow " + name + " for this session", "kind": "allow_always"},
			map[string]any{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
		},
	}
	var res struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := s.conn.Call(ctx, "session/request_permission", params, &res); err != nil {
		return false
	}
	if res.Outcome.Outcome != "selected" {
		return false
	}
	switch res.Outcome.OptionID {
	case "allow_once":
		return true
	case "allow_always":
		ss.mu.Lock()
		ss.always[name] = true
		ss.mu.Unlock()
		return true
	}
	return false
}

// toolKind maps a Belai tool onto ACP's ToolKind.
func toolKind(name string) string {
	switch name {
	case "Read", "Cat", "Head", "Tail", "RepoRead", "Skill":
		return "read"
	case "Write", "Edit", "SkillDraft":
		return "edit"
	case "Grep", "Glob", "SearchSessions", "SearchMemory":
		return "search"
	case "Bash", "ProcessRestart", "BashOutput", "KillShell":
		return "execute"
	case "WebFetch", "WebSearch":
		return "fetch"
	case "update_plan", "Task":
		return "think"
	}
	return "other"
}

func planEntries(l *todos.List) []any {
	out := make([]any, 0, len(l.Items))
	for _, it := range l.Items {
		status := "pending"
		switch it.Status {
		case todos.StatusActive:
			status = "in_progress"
		case todos.StatusDone:
			status = "completed"
		}
		out = append(out, map[string]any{"content": it.Text, "priority": "medium", "status": status})
	}
	return out
}
