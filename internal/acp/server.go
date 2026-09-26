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

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/todos"
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
}

type acpSession struct {
	id      string
	agent   *agent.Session
	mu      sync.Mutex
	history []run.Turn
	cancel  context.CancelFunc
	// always holds tool names the editor allowed for the rest of the
	// session (allow_always). It never reaches a settings file.
	always map[string]bool
}

// Serve runs the protocol on r and w until the peer disconnects.
func Serve(ctx context.Context, r io.Reader, w io.Writer, build Builder) error {
	s := &Server{build: build, sessions: map[string]*acpSession{}, ready: make(chan struct{})}
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
	}
	s.mu.Unlock()
	if err := s.conn.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Methods lists every ACP method the server answers.
var Methods = []string{"initialize", "authenticate", "session/new", "session/prompt", "session/cancel"}

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
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "belai does not support %s", method)
}

func (s *Server) initialize(params json.RawMessage) (any, error) {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"agentCapabilities": map[string]any{
			"loadSession":        false,
			"promptCapabilities": map[string]any{"image": false, "audio": false, "embeddedContext": true},
			"mcpCapabilities":    map[string]any{"http": false, "sse": false},
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
	id := session.MustID()
	ag, err := s.build(ctx, filepath.Clean(p.Cwd), id)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	s.mu.Lock()
	s.sessions[id] = &acpSession{id: id, agent: ag, always: map[string]bool{}}
	s.mu.Unlock()
	return map[string]any{"sessionId": id}, nil
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
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
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
	if text == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "empty prompt")
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

	var res run.Result
	var runErr error
	for ev := range ss.agent.RunStream(turnCtx, history, agent.TurnInput{Prompt: text}) {
		switch ev.Kind {
		case agent.EventDoneKind:
			res = ev.Result
		case agent.EventErrorKind:
			runErr = ev.Err
		default:
			s.forward(turnCtx, ss, ev)
		}
	}
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
	ss.history = append(ss.history, run.Turn{Role: "user", Content: prompt}, run.Turn{Role: "assistant", Content: res.Reply})
	ss.mu.Unlock()
	return map[string]any{"stopReason": "end_turn"}, nil
}

func (s *Server) update(ss *acpSession, u map[string]any) {
	_ = s.conn.Notify("session/update", map[string]any{"sessionId": ss.id, "update": u})
}

func textContent(t string) map[string]any { return map[string]any{"type": "text", "text": t} }

// forward maps one agent event onto session/update notifications, and a
// permission ask onto a session/request_permission request.
func (s *Server) forward(ctx context.Context, ss *acpSession, ev agent.Event) {
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
		if ev.Tool != nil {
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
	case "Bash", "ProcessRestart":
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
