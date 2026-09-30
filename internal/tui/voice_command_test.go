package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// scoreDecider answers every "s:<id>" question from a table keyed by the
// target's ID suffix in the label it was built with, so the test names targets
// by what they are.
type scoreDecider struct {
	byName map[string]float64
	calls  int
}

func (d *scoreDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.calls++
	out := map[string]decisions.Answer{}
	state, _ := r.State.(map[string]any)
	for id := range r.Questions {
		v := 0.05
		for name, score := range d.byName {
			if labelOf(state, strings.TrimPrefix(id, "s:"), name) {
				v = score
			}
		}
		out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *scoreDecider) Identity() string           { return "fake/voice" }
func (d *scoreDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

// labelOf reports whether the target with this id carries name in its label.
func labelOf(state map[string]any, id, name string) bool {
	items, _ := state["items"].(map[string]string)
	label := items[id]
	return strings.Contains(label, name)
}

func voiceJobApp(t *testing.T, d decisions.Decider) *App {
	t.Helper()
	a := gateApp(t, config.VoiceSettings{})
	a.voice.eng = nil
	a.voice.jobs = &jev.Jobs{Client: jev.NewWith(d)}
	a.voice.jobsKey = a.cfg.Classifier.Provider + "/" + a.cfg.Classifier.Model
	return a
}

func TestVoiceTargetsOfferPlainNamesAndTheStaticOnes(t *testing.T) {
	a := voiceJobApp(t, &scoreDecider{})
	var kinds []string
	for _, tg := range a.voiceTargets() {
		if strings.ContainsAny(tg.Name, " /\n") {
			t.Errorf("target name %q is not a plain identifier", tg.Name)
		}
		kinds = append(kinds, tg.Kind)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"mode", "review"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s target in %v", want, kinds)
		}
	}
}

func TestVoiceMatchableBoundsAndSwitches(t *testing.T) {
	a := voiceJobApp(t, &scoreDecider{})
	if !a.voiceMatchable("switch to plan mode") {
		t.Fatal("a short instruction was not matchable")
	}
	if a.voiceMatchable(strings.Repeat("word ", voiceCommandMaxWords+1)) {
		t.Fatal("a paragraph was offered to the backend")
	}
	off := false
	a.settings.Voice.Commands = &off
	if a.voiceMatchable("switch to plan mode") {
		t.Fatal("voice.commands off still matched")
	}
	a.settings.Voice.Commands = nil
	a.settings.Jev = &config.JevSettings{Jobs: map[string]bool{string(config.JevVoiceCommand): false}}
	if a.voiceMatchable("switch to plan mode") {
		t.Fatal("the job switch off still matched")
	}
}

func TestNoBackendMeansNoMatch(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.voice.jobs = nil
	a.voice.jobsKey = "/"
	if a.voiceMatchable("switch to plan mode") {
		t.Fatal("matched with no decision backend")
	}
	text, cmd, done := a.voiceGate("switch to plan mode")
	if done || cmd != nil || text != "switch to plan mode" {
		t.Fatalf("with no backend the speech must stay dictation, got %q done=%v", text, done)
	}
}

func matchOnce(t *testing.T, a *App, said string) voiceCommandMsg {
	t.Helper()
	text, cmd, done := a.voiceGate(said)
	if !done || cmd == nil || text != "" {
		t.Fatalf("%q was not sent to the matcher: %q %v %v", said, text, cmd != nil, done)
	}
	m, ok := cmd().(voiceCommandMsg)
	if !ok {
		t.Fatal("the match did not return a voiceCommandMsg")
	}
	return m
}

func TestOneClearTargetRunsAndNothingIsDictated(t *testing.T) {
	d := &scoreDecider{byName: map[string]float64{"mode plan": 0.98}}
	a := voiceJobApp(t, d)
	m := matchOnce(t, a, "switch to plan mode")
	if m.id == "" {
		t.Fatal("the clear target was not picked")
	}
	// handleVoiceCommand only accepts a result while voice is running.
	a2, _ := started(t, config.VoiceSettings{})
	a.voice.eng = a2.voice.eng
	a.handleVoiceCommand(m)
	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if len(a.voice.queue) != 0 || a.editor.Value() != "" {
		t.Fatal("an instruction was also dictated")
	}
}

func TestTwoCloseTargetsAreAmbiguousAndStayDictation(t *testing.T) {
	d := &scoreDecider{byName: map[string]float64{"mode plan": 0.98, "mode goal": 0.97}}
	a := voiceJobApp(t, d)
	m := matchOnce(t, a, "plan or goal")
	if m.id != "" {
		t.Fatalf("picked %q from two matches", m.id)
	}
	a2, _ := started(t, config.VoiceSettings{})
	a.voice.eng = a2.voice.eng
	a.voice.cmdGen = m.gen
	a.handleVoiceCommand(m)
	if a.mode == "plan" || a.mode == "goal" {
		t.Fatalf("an ambiguous instruction switched to %q", a.mode)
	}
}

func TestJustUnderTheCutoffStaysDictation(t *testing.T) {
	d := &scoreDecider{byName: map[string]float64{"mode plan": 0.94}}
	a := voiceJobApp(t, d)
	if m := matchOnce(t, a, "maybe plan"); m.id != "" {
		t.Fatalf("0.94 picked %q", m.id)
	}
}

func TestTypingCancelsAPendingMatch(t *testing.T) {
	d := &scoreDecider{byName: map[string]float64{"mode plan": 0.98}}
	a, _ := started(t, config.VoiceSettings{})
	a.voice.jobs = &jev.Jobs{Client: jev.NewWith(d)}
	a.voice.jobsKey = a.cfg.Classifier.Provider + "/" + a.cfg.Classifier.Model
	a.voice.ready = true
	m := matchOnce(t, a, "switch to plan mode")
	a.voice.cmdGen++ // what voiceCancelFlow does when a key is typed
	if cmd := a.handleVoiceCommand(m); cmd != nil || a.mode == "plan" {
		t.Fatal("a cancelled match still ran")
	}
}
