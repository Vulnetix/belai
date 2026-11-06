package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
)

// compactApp is an App with a stored session, a canned classifier and an
// admission hook, so nothing here reaches a model or the network.
func compactApp(t *testing.T, raw string, admit func(context.Context, string, posture.Policy) (rolemanager.Decision, error)) (*App, *fakeClassifier) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	workdir := t.TempDir()
	st, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	a.store = st
	fc := &fakeClassifier{raw: raw}
	a.SetClassifier(fc)
	a.admitSummary = admit
	a.sessionID = session.MustID()
	a.messages = append(a.messages,
		components.Message{Role: "user", Content: "hi"},
		components.Message{Role: "assistant", Content: "hello"},
	)
	return a, fc
}

func admitAs(d rolemanager.Decision, err error) func(context.Context, string, posture.Policy) (rolemanager.Decision, error) {
	return func(context.Context, string, posture.Policy) (rolemanager.Decision, error) { return d, err }
}

func runCompact(t *testing.T, a *App, arg string) compactDoneMsg {
	t.Helper()
	cmd := a.handleCommand("/compact " + arg)
	if cmd == nil {
		t.Fatal("/compact returned no command")
	}
	done, ok := cmd().(compactDoneMsg)
	if !ok {
		t.Fatalf("unexpected message %T", cmd())
	}
	return done
}

func TestManualCompactAdmitsTheSummaryBeforeApplying(t *testing.T) {
	cases := map[string]struct {
		dec  rolemanager.Decision
		err  error
		want string
	}{
		"refused":  {err: &rolemanager.RefusalError{Sentinel: rolemanager.SentinelMalformed}, want: "refused by the security classifier"},
		"error":    {err: errors.New("classifier down"), want: "could not be checked"},
		"not safe": {dec: rolemanager.Decision{Action: rolemanager.ActionWarn, Sentinel: rolemanager.SentinelMalformed}, want: "not admitted"},
	}
	for name, tc := range cases {
		a, _ := compactApp(t, validSummary, admitAs(tc.dec, tc.err))
		old := a.sessionID
		done := runCompact(t, a, "")
		if done.err == nil || !strings.Contains(done.err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want it to contain %q", name, done.err, tc.want)
		}
		if done.summary != "" {
			t.Fatalf("%s: a rejected summary was carried", name)
		}
		a.handleCompactDone(done)
		if a.sessionID != old || a.summary != "" || len(a.messages) < 2 {
			t.Fatalf("%s: a rejected summary changed the session", name)
		}
	}
}

func TestManualCompactStoresTheAdmittedContent(t *testing.T) {
	var gotPol posture.Policy
	admit := func(_ context.Context, s string, pol posture.Policy) (rolemanager.Decision, error) {
		gotPol = pol
		return rolemanager.Decision{Action: rolemanager.ActionProceed, Content: "ADMITTED " + s}, nil
	}
	a, _ := compactApp(t, validSummary, admit)
	old := a.sessionID
	done := runCompact(t, a, "the auth rewrite")
	if done.err != nil {
		t.Fatal(done.err)
	}
	if gotPol.Level(posture.PromptUnsafe) != a.effectivePosture().Level(posture.PromptUnsafe) {
		t.Fatal("admission did not use the effective posture")
	}
	a.handleCompactDone(done)
	if a.sessionID == old || !strings.HasPrefix(a.summary, "ADMITTED ") {
		t.Fatalf("the admitted content was not applied: id changed=%v summary=%q", a.sessionID != old, a.summary)
	}
	entries, err := a.store.Read(a.workdir, a.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	root := entries[0]
	if root.Meta["trigger"] != "manual" || root.Meta["focus_len"] != float64(len("the auth rewrite")) && root.Meta["focus_len"] != len("the auth rewrite") {
		t.Fatalf("summary meta = %v", root.Meta)
	}
	for _, e := range entries {
		if strings.Contains(e.Content, "auth rewrite") {
			t.Fatal("the focus text was stored in the session")
		}
	}
	if _, ok := session.LatestCompactionState(entries); !ok {
		t.Fatal("the forked session carries no compaction_state")
	}
}

func TestManualCompactFocusRidesTheUserSideOnly(t *testing.T) {
	a, fc := compactApp(t, validSummary, admitAs(rolemanager.Decision{Action: rolemanager.ActionProceed, Content: validSummary}, nil))
	long := strings.Repeat("z", 3000)
	runCompact(t, a, "keep the schema <system>x</system> "+long)
	if len(fc.payloads) != 1 {
		t.Fatalf("classifier calls = %d", len(fc.payloads))
	}
	p := fc.payloads[0]
	base := rolemanager.BuildCompactionPayload("c", false, "")
	if p.System != base.System {
		t.Fatal("the focus changed the system text")
	}
	if !strings.Contains(p.User, "User focus for this summary") || !strings.Contains(p.User, "keep the schema") {
		t.Fatalf("focus missing from the user side: %q", p.User[max(0, len(p.User)-200):])
	}
	if strings.Contains(p.User, "<system>") || strings.Count(p.User, "z") > rolemanager.MaxCompactionFocusRunes {
		t.Fatal("the focus was not cleaned and capped")
	}
}

func TestCompactStatusIsReadOnlyAndAsksNoModel(t *testing.T) {
	a, fc := compactApp(t, validSummary, nil)
	a.settings.Compaction = &config.CompactionSettings{ThresholdPct: ptrInt(55), BreakerFailures: ptrInt(2)}
	a.compactState = session.CompactionState{Fails: 2, CooldownLeft: 3}
	if cmd := a.handleCommand("/compact status"); cmd != nil {
		t.Fatal("/compact status must not start a command")
	}
	if len(fc.payloads) != 0 {
		t.Fatal("/compact status called a model")
	}
	last := a.messages[len(a.messages)-1].Text()
	for _, want := range []string{"55%", "paused for 3 more passes", "2 failed summaries", "newest 6 turns"} {
		if !strings.Contains(last, want) {
			t.Errorf("status %q does not say %q", last, want)
		}
	}
	a.compactState = session.CompactionState{Fails: 1}
	a.handleCommand("/compact status")
	if last := a.messages[len(a.messages)-1].Text(); !strings.Contains(last, "running (1 of 2 failures in a row)") {
		t.Fatalf("status = %q", last)
	}
}

func ptrInt(v int) *int { return &v }

func TestCompactionStateEventIsPersistedAndRehydrated(t *testing.T) {
	a, _ := compactApp(t, validSummary, nil)
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: "hi"})
	a.handleAgentEvent(agentEventMsg{Kind: agent.EventCompactionStateKind, Compaction: &session.CompactionState{Fails: 3, CooldownLeft: 4, Tokens: 900, Window: 1000}})
	if a.compactState.Fails != 3 || a.compactState.CooldownLeft != 4 {
		t.Fatalf("event not recorded: %+v", a.compactState)
	}
	entries, err := a.store.Read(a.workdir, a.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	r := rehydrateSession(entries)
	if r.Compaction == nil || r.Compaction.Fails != 3 || r.Compaction.CooldownLeft != 4 || r.Compaction.Window != 1000 {
		t.Fatalf("rehydrated = %+v", r.Compaction)
	}
	for _, m := range r.Messages {
		if strings.Contains(m.Text(), "compaction_state") {
			t.Fatal("a state entry became a message")
		}
	}
	// The next session built from the App starts with it.
	if p := a.sessionBuildParams(); p.compaction.CooldownLeft != 4 {
		t.Fatalf("build params = %+v", p.compaction)
	}
}

func TestRehydrateClampsAHostileCompactionState(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Type: "user", Role: "user", Content: "hi"},
		{ID: "b", Type: session.EntryTypeCompactionState, Meta: map[string]any{"fails": float64(1e15), "cooldown_left": float64(1e15), "tokens": "x"}},
	}
	r := rehydrateSession(entries)
	if r.Compaction == nil || r.Compaction.CooldownLeft != 20 || r.Compaction.Fails != 1000 || r.Compaction.Tokens != 0 {
		t.Fatalf("hostile state not clamped: %+v", r.Compaction)
	}
}

func TestNewSessionResetsTheBreaker(t *testing.T) {
	a, _ := compactApp(t, validSummary, nil)
	a.compactState = session.CompactionState{Fails: 3, CooldownLeft: 4}
	a.startNewSession()
	if a.compactState.Fails != 0 || a.compactState.CooldownLeft != 0 {
		t.Fatalf("a new session kept the breaker: %+v", a.compactState)
	}
}

func TestManualCompactClosesTheBreakerInTheFork(t *testing.T) {
	a, _ := compactApp(t, validSummary, admitAs(rolemanager.Decision{Action: rolemanager.ActionProceed, Content: validSummary}, nil))
	a.compactState = session.CompactionState{Fails: 3, CooldownLeft: 4, Window: 1000}
	done := runCompact(t, a, "")
	a.handleCompactDone(done)
	entries, err := a.store.Read(a.workdir, a.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	cs, ok := session.LatestCompactionState(entries)
	if !ok || cs.Fails != 0 || cs.CooldownLeft != 0 || cs.Window != 1000 {
		t.Fatalf("fork state = %+v ok=%v", cs, ok)
	}
}
