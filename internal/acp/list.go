package acp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/jsonrpc"
)

// listSessions answers session/list with the sessions open on this
// connection. It reads no store and takes no path beyond an optional cwd
// filter compared as text, so it is not a way to enumerate anything else.
// Belai keeps no resumable sessions (loadSession is false), so the list is
// only what the editor already started.
func (s *Server) listSessions(params json.RawMessage) (any, error) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
		}
	}
	if p.Cwd != "" && !filepath.IsAbs(p.Cwd) {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cwd must be an absolute path")
	}
	want := ""
	if p.Cwd != "" {
		want = filepath.Clean(p.Cwd)
	}
	type row struct {
		at   time.Time
		info map[string]any
	}
	var rows []row
	s.mu.Lock()
	client := s.client
	for _, ss := range s.sessions {
		if want != "" && ss.cwd != want {
			continue
		}
		ss.mu.Lock()
		at, turns := ss.updated, len(ss.history)/2
		ss.mu.Unlock()
		rows = append(rows, row{at: at, info: map[string]any{
			"sessionId": ss.id,
			"cwd":       ss.cwd,
			"title":     sessionTitle(client, ss.cwd, turns),
			"updatedAt": at.UTC().Format(time.RFC3339),
		}})
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].at.After(rows[j].at) })
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.info)
	}
	return map[string]any{"sessions": out}, nil
}

// sessionTitle is harness text only: the editor's name, the directory and the
// number of turns. It never carries a prompt or model output.
func sessionTitle(client, cwd string, turns int) string {
	noun := "turns"
	if turns == 1 {
		noun = "turn"
	}
	return sessionName(client, fmt.Sprintf("%s, %d %s", dirName(cwd), turns, noun))
}

// closeSession answers session/close: it stops a running turn, ends the
// session's transcript recording and forgets it.
func (s *Server) closeSession(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
	}
	s.mu.Lock()
	ss := s.sessions[p.SessionID]
	delete(s.sessions, p.SessionID)
	s.mu.Unlock()
	if ss == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown session %q", p.SessionID)
	}
	ss.mu.Lock()
	if ss.cancel != nil {
		ss.cancel()
	}
	ss.mu.Unlock()
	ss.log.Flush()
	s.touch(ss)
	if ss.detach != nil {
		ss.detach()
		s.mu.Lock()
		s.recording = false
		s.mu.Unlock()
	}
	return map[string]any{}, nil
}
