package acp

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/jsonrpc"
)

// capEditor answers session/request_permission with answer (an option id) or a
// cancelled outcome, and keeps the options it was offered.
type capEditor struct {
	mu      sync.Mutex
	answer  string
	cancel  bool
	options []map[string]any
	title   string
}

func (e *capEditor) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, nil
	}
	var p struct {
		ToolCall map[string]any   `json:"toolCall"`
		Options  []map[string]any `json:"options"`
	}
	_ = json.Unmarshal(params, &p)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.options = p.Options
	e.title, _ = p.ToolCall["title"].(string)
	if e.cancel {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": e.answer}}, nil
}

func capServer(t *testing.T, ed *capEditor) *Server {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	s := &Server{sessions: map[string]*acpSession{}, ready: make(chan struct{})}
	s.conn = jsonrpc.NewConn(sr, sw, s.handle)
	close(s.ready)
	editor := jsonrpc.NewConn(cr, cw, ed.handle)
	t.Cleanup(func() { s.conn.Close(); editor.Close() })
	return s
}

func capEvent() agent.Event {
	return agent.Event{ToolCallID: "c1", Ask: &agent.AskRequest{
		Name:   agent.RunCapAskName,
		RunCap: &agent.RunCapAsk{Reason: "cost", Used: 1_100_000, Limit: 1_000_000, ExtendBy: 500_000, ExtendsLeft: 2},
	}}
}

// R13: the editor is offered extend, stop and stop and keep, and only an
// explicit selection of extend or keep acts; anything else stops.
func TestRunCapsRule13_ACPOffersThreeAnswersAndFailsToStop(t *testing.T) {
	cases := map[string]agent.RunCapChoice{
		"extend":     agent.RunCapExtend,
		"stop":       agent.RunCapStop,
		"stop_keep":  agent.RunCapStopKeep,
		"allow_once": agent.RunCapStop, // an option the cap ask never offered
		"":           agent.RunCapStop,
		"EXTEND":     agent.RunCapStop,
	}
	for answer, want := range cases {
		ed := &capEditor{answer: answer}
		s := capServer(t, ed)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got := s.askRunCap(ctx, &acpSession{id: "s1"}, capEvent())
		cancel()
		if got != want {
			t.Errorf("answer %q: %q, want %q", answer, got, want)
		}
		if len(ed.options) != 3 || ed.options[0]["optionId"] != "extend" || ed.options[1]["optionId"] != "stop" || ed.options[2]["optionId"] != "stop_keep" {
			t.Errorf("options %v", ed.options)
		}
		if ed.title != "Run cap reached: cost $1.10 of $1.00. Extend by $0.50 (2 left), stop now, or stop and keep the counters?" {
			t.Errorf("title %q", ed.title)
		}
	}
	ed := &capEditor{cancel: true}
	s := capServer(t, ed)
	if got := s.askRunCap(context.Background(), &acpSession{id: "s1"}, capEvent()); got != agent.RunCapStop {
		t.Errorf("a cancelled ask answered %q", got)
	}
	// A dead connection stops the run too.
	dead := capServer(t, &capEditor{})
	dead.conn.Close()
	if got := dead.askRunCap(context.Background(), &acpSession{id: "s1"}, capEvent()); got != agent.RunCapStop {
		t.Errorf("a closed connection answered %q", got)
	}
}
