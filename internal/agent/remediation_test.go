package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/vulnid"
)

// A remediation turn is pushed to edit after remediationNudgeAfter rounds, not
// readStreakNudgeAfter, with the firm directive in agent mode too.
func TestRemediationNudgesSooner(t *testing.T) {
	s := &Session{turnRemediation: true}
	for _, mode := range []modes.Mode{modes.ModeAgent, modes.ModeGoal} {
		var streak, seen int
		var fired []int
		for i := 1; i <= 2*remediationNudgeAfter; i++ {
			if got := s.readStreakNudge(&streak, &seen, 0, true, mode); got != "" {
				if got != remediationNudge {
					t.Fatalf("%s: nudge = %q", mode, got)
				}
				fired = append(fired, i)
			}
		}
		if len(fired) != 2 || fired[0] != remediationNudgeAfter || fired[1] != 2*remediationNudgeAfter {
			t.Fatalf("%s: nudges at %v, want rounds %d and %d", mode, fired, remediationNudgeAfter, 2*remediationNudgeAfter)
		}
	}
	// A read-only turn is never told to edit.
	ro := &Session{turnRemediation: true, turnReadOnly: true}
	var streak, seen int
	for i := 0; i < 2*remediationNudgeAfter; i++ {
		if ro.readStreakNudge(&streak, &seen, 0, true, modes.ModeAgent) != "" {
			t.Fatal("a read-only turn was nudged to edit")
		}
	}
}

func TestRefuseRemediationFinish(t *testing.T) {
	s := &Session{turnRemediation: true}
	if !s.refuseRemediationFinish(0, modes.ModeAgent) {
		t.Fatal("a remediation finish with no edit was accepted")
	}
	if s.refuseRemediationFinish(0, modes.ModeAgent) {
		t.Fatal("the finish was refused more than remediationGuards times")
	}
	for name, s := range map[string]*Session{
		"not remediation": {},
		"plan":            {turnRemediation: true, planMode: true},
		"read only":       {turnRemediation: true, turnReadOnly: true},
		"wrap up":         {turnRemediation: true, kanbanWrapUpPass: true},
	} {
		if s.refuseRemediationFinish(0, modes.ModeAgent) {
			t.Errorf("%s: finish refused", name)
		}
	}
	if (&Session{turnRemediation: true}).refuseRemediationFinish(1, modes.ModeAgent) {
		t.Error("a finish after an edit was refused")
	}
	if (&Session{turnRemediation: true}).refuseRemediationFinish(0, modes.ModeGoal) {
		t.Error("goal mode has its own no-write escalation")
	}
}

// A pass whose model answers with a report and no edit is sent back once, with
// the finish guard in a sealed directive, and the second answer ends it.
func TestRemediationPassRefusesAReportWithNoEdit(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		writeChatJSON(w, "Here is the report.")
	}))
	defer srv.Close()

	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: t.TempDir(), MaxBytes: 1024}),
		Posture:       posture.AllIgnore(),
		MaxIterations: 6,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sess.turnRemediation = true
	out, _, err := sess.pass(context.Background(), rolemanager.NewPipeline(nil), "system", []run.Turn{{Role: "user", Content: vulnid.RemediationPrompt("GHSA-5cv4-jp36-h3mw")}}, false, func(Event) {}, modes.ModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if out.reply != "Here is the report." {
		t.Fatalf("reply = %q", out.reply)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("model called %d times, want 2 (the report, then the answer to the guard)", len(bodies))
	}
	if strings.Contains(bodies[0], "ends the remediation with no file changed") || !strings.Contains(bodies[1], "ends the remediation with no file changed") {
		t.Fatal("the finish guard did not ride the second request alone")
	}
}

// The prepared prompt, a pasted row and a fix request run as remediation; a
// question about the advisory does not.
func TestRemediationTurnIsTheHarnessCheck(t *testing.T) {
	id := "GHSA-5cv4-jp36-h3mw"
	for _, p := range []string{vulnid.RemediationPrompt(id), vulnid.URL(id) + "\n" + vulnid.Command(id), "fix " + id} {
		if !remediationTurn(p) {
			t.Errorf("%q is not a remediation turn", p)
		}
	}
	if remediationTurn("what is " + id) {
		t.Error("a question is a remediation turn")
	}
}

// A remediation prompt reaches the model as an agent turn with the contract in
// a sealed directive, and no mode-selection request is ever made for it.
func TestRemediationTurnSkipsModeSelection(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		writeChatJSON(w, "I quote go.mod line 7: golang.org/x/net v0.59.0 is already the fixed version.")
	}))
	defer srv.Close()

	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:          run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:       srv.Client(),
		Registry:     tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, &tools.Edit{Root: root}),
		Posture:      posture.AllIgnore(),
		AllowExplore: true,
		AllowClarify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decided *rolemanager.ModeDecision
	_, err = sess.RunInputObserved(context.Background(), nil, TurnInput{Prompt: vulnid.URL("GHSA-5cv4-jp36-h3mw") + "\n" + vulnid.Command("GHSA-5cv4-jp36-h3mw")}, func(ev Event) {
		if ev.Kind == EventModeDecidedKind && ev.Mode != nil {
			decided = ev.Mode
		}
		if ev.Kind == EventClarifyAskKind {
			t.Error("a remediation prompt raised a question")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided == nil || decided.Mode != modes.ModeAgent || decided.UserChosen {
		t.Fatalf("mode decision = %+v, want agent with no choice asked", decided)
	}
	mu.Lock()
	defer mu.Unlock()
	var carried bool
	for _, b := range bodies {
		if strings.Contains(b, "operating-mode classifier") {
			t.Fatal("the mode classifier ran for a remediation prompt")
		}
		carried = carried || strings.Contains(b, "Remediation turn:")
	}
	if !carried {
		t.Fatal("no request carried the remediation directive")
	}
}
