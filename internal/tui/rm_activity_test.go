package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// TestRMActivityObserverFeedsPanel pins the transport wiring: New registers
// the observer, a role-manager decision lands on the buffered channel, and
// addRMActivity turns it into a render-only rolemanager message.
func TestRMActivityObserverFeedsPanel(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	defer a.rmCancel()

	// ParseSessionName records a session_name decision through the global
	// observer registered by New.
	if _, err := rolemanager.ParseSessionName("my session"); err != nil {
		t.Fatalf("ParseSessionName: %v", err)
	}

	cmd := a.nextRMActivity()
	if cmd == nil {
		t.Fatal("nextRMActivity returned nil")
	}
	msg, ok := cmd().(rmActivityMsg)
	if !ok {
		t.Fatalf("nextRMActivity delivered %T, want rmActivityMsg", msg)
	}
	a.addRMActivity(rolemanager.Activity(msg))

	found := false
	for _, m := range a.messages {
		if m.Role == "rolemanager" && m.RM.Summary != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rolemanager message appended: %+v", a.messages)
	}
}

// A role-manager row is labelled with the model that answered it; only an
// activity with no served model falls back to the agent model. Model ids that
// contain slashes (@cf/org/name) split on the first slash only.
func TestRMActivityLabelsTheServedModel(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	defer a.rmCancel()
	a.cfg.Provider, a.cfg.Model = "cloudflare-ai-gateway", "@cf/deepseek-ai/deepseek-v4-pro-0813"

	a.addRMActivity(rolemanager.Activity{Event: rolemanager.EventSessionName, Verdict: "valid", Model: "cloudflare-workers-ai/@cf/deepseek-ai/deepseek-v4-flash-0731"})
	a.addRMActivity(rolemanager.Activity{Event: rolemanager.EventSessionName, Verdict: "valid"})

	var rows []string
	for _, m := range a.messages {
		if m.Role == "rolemanager" {
			rows = append(rows, m.Provider+" | "+m.Model)
		}
	}
	want := []string{
		"cloudflare-workers-ai | @cf/deepseek-ai/deepseek-v4-flash-0731",
		"cloudflare-ai-gateway | @cf/deepseek-ai/deepseek-v4-pro-0813",
	}
	if len(rows) != 2 || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}
