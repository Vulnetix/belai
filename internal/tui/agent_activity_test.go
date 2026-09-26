package tui

import (
	"errors"
	"testing"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/agent"
)

func agentActivity(t *testing.T, a *App, key string) activity.Activity {
	t.Helper()
	for _, act := range a.activity.List() {
		if act.Kind == activity.KindAgent && act.Label == key {
			return act
		}
	}
	t.Fatalf("no runs-panel row labelled %q", key)
	return activity.Activity{}
}

// A background agent's row is labelled with its key, so two scanner agents
// of one profile are told apart, and is quiet: the agent's own start line is
// the only one in the main thread.
func TestAgentActivityLabelledByKeyAndQuiet(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.registerAgentActivity("belai:vulnetix-scanner@sast#1", a.workdir)
	a.registerAgentActivity("belai:vulnetix-scanner@cbom#1", a.workdir)
	for _, key := range []string{"belai:vulnetix-scanner@sast#1", "belai:vulnetix-scanner@cbom#1"} {
		act := agentActivity(t, a, key)
		if act.State != activity.StateRunning || !act.Quiet {
			t.Fatalf("%s: state %s quiet %v", key, act.State, act.Quiet)
		}
	}
}

// The row closes when the agent's loop ends: done after a reply, failed after
// an error with no reply, killed when the user stopped it. It used to stay
// "running" for the rest of the session.
func TestAgentActivityFinishesWithTheAgent(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})

	a.registerAgentActivity("ok", a.workdir)
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "ok", Kind: agent.EventDoneKind})
	if s := agentActivity(t, a, "ok").State; s != activity.StateDone {
		t.Fatalf("finished agent row = %s, want done", s)
	}

	a.registerAgentActivity("broken", a.workdir)
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "broken", Kind: agent.EventErrorKind, Err: errors.New("provider unavailable")})
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "broken", Kind: agent.EventDoneKind})
	if s := agentActivity(t, a, "broken").State; s != activity.StateFailed {
		t.Fatalf("errored agent row = %s, want failed", s)
	}

	a.registerAgentActivity("stopped", a.workdir)
	a.noteAgentStopped("stopped")
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "stopped", Kind: agent.EventDoneKind})
	if s := agentActivity(t, a, "stopped").State; s != activity.StateKilled {
		t.Fatalf("stopped agent row = %s, want killed", s)
	}
	if len(a.agentActs) != 0 {
		t.Fatalf("closed rows must be forgotten: %v", a.agentActs)
	}
}
