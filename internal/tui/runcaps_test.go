package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/voicecmd"
)

func runCapRequest() *agent.AskRequest {
	return &agent.AskRequest{
		Name: agent.RunCapAskName, Subject: "tokens",
		RunCap: &agent.RunCapAsk{Reason: "tokens", Used: 2100, Limit: 2000, ExtendBy: 1000, ExtendsLeft: 3},
	}
}

func openRunCapAsk(t *testing.T) (*App, chan agent.PermissionAskReply) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply, 1)
	a.permAskState = newPermissionAskState(runCapRequest(), reply)
	return a, reply
}

func waitReply(t *testing.T, ch chan agent.PermissionAskReply) agent.PermissionAskReply {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(time.Second):
		t.Fatal("no reply sent")
	}
	return agent.PermissionAskReply{}
}

// R4: the ask offers extend, stop now and stop and keep state, with the safe
// row highlighted, and the view says what was reached.
func TestRunCapsRule4_AskViewAndOptions(t *testing.T) {
	a, _ := openRunCapAsk(t)
	st := a.permAskState
	opts := st.options()
	if len(opts) != 3 || opts[0] != "extend" || opts[1] != "stop now" || opts[2] != "stop and keep state" {
		t.Fatalf("options %v", opts)
	}
	if st.selected != 1 {
		t.Fatalf("the highlighted row is %d: enter pressed unseen must not extend", st.selected)
	}
	if st.decisionAt(0) != askRunCapExtend || st.decisionAt(1) != askDeny || st.decisionAt(2) != askRunCapKeep {
		t.Fatal("rows map to the wrong decisions")
	}
	out := a.permissionAskView()
	for _, want := range []string{"Run cap reached", "tokens 2100 tokens of 2000 tokens", "extend", "stop now", "stop and keep state"} {
		if !strings.Contains(out, want) {
			t.Fatalf("view lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "allow always") {
		t.Fatalf("a run cap offers no allow always:\n%s", out)
	}
}

// R4: the keyboard extends; a bare enter stops.
func TestRunCapsRule4_KeysExtendAndEnterStops(t *testing.T) {
	a, reply := openRunCapAsk(t)
	ch := make(chan agent.Event)
	a.events = ch
	close(ch)
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if r := waitReply(t, reply); r.Choice != agent.RunCapStop || r.Allow {
		t.Fatalf("a bare enter answered %+v", r)
	}

	a, reply = openRunCapAsk(t)
	a.events = ch
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if r := waitReply(t, reply); r.Choice != agent.RunCapExtend {
		t.Fatalf("up then enter answered %+v", r)
	}

	a, reply = openRunCapAsk(t)
	a.events = ch
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if r := waitReply(t, reply); r.Choice != agent.RunCapStopKeep {
		t.Fatalf("down then enter answered %+v", r)
	}

	a, reply = openRunCapAsk(t)
	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if r := waitReply(t, reply); r.Choice != agent.RunCapStop {
		t.Fatalf("esc answered %+v", r)
	}
}

// E8: a website answer or a spoken keyword never extends, whatever it says.
func TestRunCapsEdge8_WebAndVoiceNeverExtend(t *testing.T) {
	for _, src := range []string{answerFromWeb, answerFromVoice, answerFromClef, "something-else"} {
		for _, decision := range []string{askRunCapExtend, askAllowOnce, askAllowAlways, askRunCapKeep} {
			a, reply := openRunCapAsk(t)
			a.settlePermissionAsk(decision, src, "")
			r := waitReply(t, reply)
			if r.Choice == agent.RunCapExtend || r.Allow {
				t.Errorf("%s answering %s extended: %+v", src, decision, r)
			}
		}
	}
	// A spoken approval is ordinary speech while a cap is open.
	a, reply := openRunCapAsk(t)
	if did, _ := a.voiceKeyword(voicecmd.Approve, 0); did {
		t.Fatal("a spoken approve acted on a run-cap ask")
	}
	select {
	case r := <-reply:
		t.Fatalf("the ask was answered: %+v", r)
	case <-time.After(30 * time.Millisecond):
	}
	// Turning ask off does not answer it.
	a.resolvePendingAsk(true)
	if a.permAskState.reply == nil {
		t.Fatal("ask off dismissed a run-cap ask")
	}
}

// R16: /caps shows numbers and extends only when the user types it.
func TestRunCapsRule16_CapsCommand(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.capsCommand("")
	if got := lastSystemMessage(a); !strings.Contains(got, "run caps: off") {
		t.Fatalf("off message %q", got)
	}
	tokens := 2000
	a.settings.RunCaps = &config.RunCapsSettings{MaxTokens: &tokens}
	a.capsCommand("status")
	if got := lastSystemMessage(a); !strings.Contains(got, "tokens 0 of 2000") || !strings.Contains(got, "3 of 3 extensions left") {
		t.Fatalf("status %q", got)
	}
	a.capsCommand("extend")
	if got := lastSystemMessage(a); !strings.Contains(got, "tokens 0 of 3000") {
		t.Fatalf("extend %q", got)
	}
	if a.runCaps.ExtendsLeft() != 2 {
		t.Fatalf("extends left %d", a.runCaps.ExtendsLeft())
	}
	a.capsCommand("bogus")
	if got := lastSystemMessage(a); !strings.Contains(got, "usage: /caps") {
		t.Fatalf("usage %q", got)
	}
	// A settings change rebuilds the session with the same meter and counters.
	m := a.runCaps
	m.Spend(0, 500)
	a.sessionBuildParams()
	if a.runCaps != m || a.runCaps.Snapshot().Tokens != 500 || a.runCaps.Caps().Tokens != 3000 {
		t.Fatalf("a rebuild lost the meter: %+v", a.runCaps.Snapshot())
	}
	// A new session id gets its own meter.
	a.sessionID = "another-session"
	if a.runCapsMeter() == m {
		t.Fatal("a new session shares the old meter")
	}
	// Caps off again: no meter.
	a.settings.RunCaps = nil
	if a.runCapsMeter() != nil {
		t.Fatal("a meter outlived its caps")
	}
}

func TestRunCapsRule2_FooterChip(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	if label, _ := a.capFooterLabel(); label != "" {
		t.Fatalf("a chip with no cap: %q", label)
	}
	tokens := 1000
	a.settings.RunCaps = &config.RunCapsSettings{MaxTokens: &tokens}
	if label, warn := a.capFooterLabel(); label != "cap tokens 0%" || warn {
		t.Fatalf("chip %q %v", label, warn)
	}
	a.runCaps.Spend(0, 950)
	if label, warn := a.capFooterLabel(); label != "cap tokens 95%" || !warn {
		t.Fatalf("chip %q %v", label, warn)
	}
}
