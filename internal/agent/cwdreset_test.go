package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// cwdSession is a session whose model calls Cd into each of dirs, one per
// model request that follows a tool result, then answers.
func cwdSession(t *testing.T, root string, dirs []string) *Session {
	t.Helper()
	var mu sync.Mutex
	step := 0
	off := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system := ""
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		if strings.Contains(system, "security classifier") {
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if step < len(dirs) {
			args, _ := json.Marshal(map[string]any{"path": dirs[step]})
			step++
			cwdToolCall(w, req.Stream, string(args))
			return
		}
		cwdAnswer(w, req.Stream)
	}))
	t.Cleanup(srv.Close)
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.DefaultWithCaps(root, false, tools.CapabilitiesOf(nil, nil), repoindex.Index{}),
		Perms:         permissions.Settings{Allow: []string{"Cd"}},
		Settings:      config.Settings{DeferTools: &off},
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		Workdir:       root,
		MaxIterations: 10,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// cwdToolCall answers with a Cd call, as an event stream when the client asked
// for one (RunStream does) and as plain JSON otherwise.
func cwdToolCall(w http.ResponseWriter, stream bool, args string) {
	if !stream {
		writeToolCallJSON(w, "Cd", args)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	call, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
		"index": 0, "id": "call_1", "type": "function",
		"function": map[string]any{"name": "Cd", "arguments": args},
	}}}}}})
	fmt.Fprintf(w, "data: %s\n\n", call)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func cwdAnswer(w http.ResponseWriter, stream bool) {
	if !stream {
		writeChatJSON(w, "done")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func cwdRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"sub/inner", "other"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func collect(sess *Session, prompt string) []Event {
	var events []Event
	for e := range sess.RunStream(context.Background(), nil, TurnInput{Prompt: prompt, ForceMode: modes.ModeAgent}) {
		events = append(events, e)
	}
	return events
}

func cwdEvents(events []Event) (moves, resets []Event) {
	for _, e := range events {
		if e.Kind != EventCwdKind {
			continue
		}
		if e.CwdReset {
			resets = append(resets, e)
		} else {
			moves = append(moves, e)
		}
	}
	return
}

func TestTurnEndPutsTheWorkingDirectoryBack(t *testing.T) {
	root := cwdRoot(t)
	sess := cwdSession(t, root, []string{"sub", "inner"})
	events := collect(sess, "go look")

	moves, resets := cwdEvents(events)
	if len(moves) != 2 || moves[1].Cwd != "sub/inner" {
		t.Fatalf("moves = %+v", moves)
	}
	if len(resets) != 1 || resets[0].Cwd != "" || resets[0].CwdDir != root {
		t.Fatalf("resets = %+v, want one back to the root %s", resets, root)
	}
	if got := sess.Cwd().Rel(); got != "" {
		t.Fatalf("tracker Rel = %q after the turn, want the root", got)
	}

	// The reset comes after the last move and before the turn is reported done.
	lastMove, resetAt, doneAt := -1, -1, -1
	for i, e := range events {
		switch {
		case e.Kind == EventCwdKind && !e.CwdReset:
			lastMove = i
		case e.Kind == EventCwdKind && e.CwdReset:
			resetAt = i
		case e.Kind == EventDoneKind:
			doneAt = i
		}
	}
	if !(lastMove < resetAt && resetAt < doneAt) {
		t.Fatalf("order: last move %d, reset %d, done %d", lastMove, resetAt, doneAt)
	}
}

func TestTurnThatNeverMovedEmitsNoReset(t *testing.T) {
	sess := cwdSession(t, cwdRoot(t), nil)
	moves, resets := cwdEvents(collect(sess, "just answer"))
	if len(moves) != 0 || len(resets) != 0 {
		t.Fatalf("moves %+v resets %+v", moves, resets)
	}
}

func TestNextTurnStartsWhereTheLastOneStartedNotWhereItEnded(t *testing.T) {
	root := cwdRoot(t)
	sess := cwdSession(t, root, []string{"sub"})
	collect(sess, "first")
	if sess.Cwd().Rel() != "" {
		t.Fatalf("after turn one Rel = %q", sess.Cwd().Rel())
	}
	// A move made between turns, as the TUI's worktree switch does, is where the
	// next turn starts and ends.
	if _, err := sess.Cwd().Change("other"); err != nil {
		t.Fatal(err)
	}
	collect(sess, "second")
	if got := sess.Cwd().Rel(); got != "other" {
		t.Fatalf("after turn two Rel = %q, want the directory it started in", got)
	}
}

func TestCancelledTurnStillRestoresTheTracker(t *testing.T) {
	root := cwdRoot(t)
	sess := cwdSession(t, root, []string{"sub"})
	ctx, cancel := context.WithCancel(context.Background())
	ch := sess.RunStream(ctx, nil, TurnInput{Prompt: "go", ForceMode: modes.ModeAgent})
	for e := range ch {
		if e.Kind == EventCwdKind && !e.CwdReset {
			cancel()
		}
	}
	cancel()
	if got := sess.Cwd().Rel(); got != "" {
		t.Fatalf("tracker Rel = %q after a cancelled turn, want the root", got)
	}
}
