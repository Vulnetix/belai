package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func testControls(allowOff bool) *WorkerControls {
	env := sessionctl.Env{
		AllowGuardrailsOff: allowOff,
		CheckModel: func(p, m, _ string) string {
			if p == "anthropic" {
				return ""
			}
			return "no credentials for " + p
		},
		Efforts: func(string, string) []string { return []string{"low", "medium", "high"} },
	}
	resolve := func(st sessionctl.State) (run.Config, error) {
		return run.Config{Provider: st.Provider, Model: st.Model}, nil
	}
	return NewWorkerControls(config.Settings{}, run.Config{Provider: "anthropic", Model: "claude-sonnet-5-5"}, env, resolve)
}

func TestWorkerControlsApplyFromTheNextTurn(t *testing.T) {
	c := testControls(false)
	base := posture.Defaults()

	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/caveman on"}); err != nil {
		t.Fatalf("caveman: %v", err)
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/model anthropic claude-opus-5-5"}); err != nil {
		t.Fatalf("model: %v", err)
	}
	s, cfg, pol := c.Turn(config.Settings{}, run.Config{Provider: "anthropic", Model: "claude-sonnet-5-5"}, base)
	if !s.CavemanEnabled() {
		t.Error("caveman is not on for the next turn")
	}
	if cfg.Model != "claude-opus-5-5" {
		t.Errorf("next turn model = %q, want claude-opus-5-5", cfg.Model)
	}
	if pol.Level(posture.ToolResultUnsafe) == posture.Ignore && base.Level(posture.ToolResultUnsafe) != posture.Ignore {
		t.Error("guardrails were dropped without a control turning them off")
	}
	if f := c.Facts(); f["model"] != "claude-opus-5-5" || f["caveman"] != true {
		t.Errorf("facts = %v", f)
	}
}

func TestWorkerControlsRefuseWhatAWorkerDoesNotTake(t *testing.T) {
	c := testControls(false)

	for _, line := range []string{"/ask on", "/mode plan", "/reasoning shown"} {
		if _, err := c.Apply(sessionsync.RemoteCommand{Line: line}); !errors.Is(err, ErrNotForWorker) {
			t.Errorf("%s: err = %v, want ErrNotForWorker", line, err)
		}
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/guardrails off"}); err == nil {
		t.Error("guardrails off was taken without the host allowing it")
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/model openai gpt-x"}); err == nil {
		t.Error("a model the host has no credentials for was taken")
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "rm -rf /"}); err == nil {
		t.Error("a line that is not a control was taken")
	}
}

func TestWorkerControlsTurnGuardrailsOffWhenTheHostAllows(t *testing.T) {
	c := testControls(true)

	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/guardrails off"}); err != nil {
		t.Fatalf("guardrails off: %v", err)
	}
	_, _, pol := c.Turn(config.Settings{}, run.Config{}, posture.Defaults())
	if pol.Level(posture.ToolResultUnsafe) != posture.Ignore {
		t.Error("guardrails off did not ignore the posture for the next turn")
	}
}

type fakeControlMirror struct {
	ch     chan sessionsync.RemoteCommand
	acks   chan [3]string
	states int
}

func (m *fakeControlMirror) Commands() <-chan sessionsync.RemoteCommand { return m.ch }
func (m *fakeControlMirror) AckCommand(id, status, reason string, _ json.RawMessage) {
	m.acks <- [3]string{id, status, reason}
}
func (m *fakeControlMirror) SetSessionControls(json.RawMessage, bool) { m.states++ }

func TestWorkerRunControlsAcksEachCommand(t *testing.T) {
	w := &Worker{Controls: testControls(false), Log: io.Discard}
	m := &fakeControlMirror{ch: make(chan sessionsync.RemoteCommand, 2), acks: make(chan [3]string, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.runControls(ctx, m)

	m.ch <- sessionsync.RemoteCommand{ID: "c1", Line: "/caveman on"}
	m.ch <- sessionsync.RemoteCommand{ID: "c2", Line: "/ask on"}
	for _, want := range [][2]string{{"c1", sessionsync.AckAccepted}, {"c2", sessionsync.AckRefused}} {
		select {
		case got := <-m.acks:
			if got[0] != want[0] || got[1] != want[1] {
				t.Errorf("ack = %v, want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no ack")
		}
	}
	cancel()
	if m.states != 1 {
		t.Errorf("controls re-registered %d times, want 1 (the accepted one)", m.states)
	}
}
