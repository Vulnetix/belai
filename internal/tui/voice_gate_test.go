package tui

import (
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
)

func gateApp(t *testing.T, v config.VoiceSettings) *App {
	t.Helper()
	a, _ := voiceApp(t, v)
	a.voice.mode = config.VoiceModeListen
	a.voice.ready = true
	return a
}

func TestWakeWordDropsUnmatchedSpeechUnheard(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{WakeWord: on(), Mode: config.VoiceModeListen})
	for _, said := range []string{"what a lovely day", "hey there belay", "submit the report by friday"} {
		text, cmd, done := a.voiceGate(said)
		if !done || cmd != nil || text != "" {
			t.Fatalf("%q was not dropped: %q %v %v", said, text, cmd != nil, done)
		}
	}
	if len(a.voice.queue) != 0 || a.editor.Value() != "" {
		t.Fatalf("unmatched speech reached the queue %v or composer %q", a.voice.queue, a.editor.Value())
	}
	if a.voiceTranscript("nothing to see here"); len(a.voice.queue) != 0 || a.voice.live.active {
		t.Fatal("a dropped transcript was queued")
	}
}

func TestWakeWordIsStrippedBeforeAnythingElse(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{WakeWord: on(), Mode: config.VoiceModeListen})
	text, _, done := a.voiceGate("Hey, Belay. Fix the failing test.")
	if done || text != "fix the failing test" {
		t.Fatalf("got %q done=%v", text, done)
	}
}

func TestBareWakeWordArmsTheNextUtteranceOnce(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{WakeWord: on(), Mode: config.VoiceModeListen})
	if _, _, done := a.voiceGate("hey belay"); !done {
		t.Fatal("a bare wake word was dictated")
	}
	text, _, done := a.voiceGate("run the tests")
	if done || text != "run the tests" {
		t.Fatalf("armed utterance = %q done=%v", text, done)
	}
	if _, _, done := a.voiceGate("and another thing"); !done {
		t.Fatal("the window stayed open after one utterance")
	}
	a.voiceGate("hey belay")
	a.voice.wakeUntil = time.Now().Add(-time.Second)
	if _, _, done := a.voiceGate("too late"); !done {
		t.Fatal("an expired window still passed speech")
	}
}

func TestKeywordWithNothingOpenIsOrdinarySpeech(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	for _, said := range []string{"stop", "deny", "option 2", "submit"} {
		text, _, done := a.voiceGate(said)
		if done || text != said {
			t.Fatalf("%q with no ask open: %q done=%v", said, text, done)
		}
	}
}

func TestStopInterruptsARunningTurnOnly(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	called := false
	a.cancel = func() { called = true }
	if _, _, done := a.voiceGate("Stop."); !done || !called {
		t.Fatalf("stop did not interrupt: done=%v called=%v", done, called)
	}
	if a.working() {
		t.Fatal("the turn is still working")
	}
}

func TestClarifyOptionSelectsAndProceedsWhenOneGroup(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.voice.ready = false // an open ask owns the screen
	a.push(viewClarify)
	reply := make(chan clarify.Answers, 1)
	q := clarify.Questionnaire{Groups: []clarify.Group{{Context: "Which?", Options: []clarify.Option{{Label: "a"}, {Label: "b"}, {Label: "c"}}}}}
	a.clarifyState = newClarifyState(q, reply, false)
	if !a.voiceCommandReady() {
		t.Fatal("an open clarify did not keep the microphone ready for keywords")
	}
	if _, _, done := a.voiceGate("option 4"); !done || a.clarifyState.reply == nil {
		t.Fatal("option 4 of 3 was accepted")
	}
	if _, _, done := a.voiceGate("Option two."); !done {
		t.Fatal("option 2 was not consumed")
	}
	select {
	case ans := <-reply:
		if len(ans.Items) != 1 || len(ans.Items[0].Chosen) != 1 || ans.Items[0].Chosen[0] != 1 {
			t.Fatalf("answers = %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("option 2 did not send the answers")
	}
	if a.view != viewChat {
		t.Fatalf("view = %v after the answer", a.view)
	}
}

func TestClarifyWithSeveralGroupsWaitsForSubmit(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.voice.ready = false
	a.push(viewClarify)
	reply := make(chan clarify.Answers, 1)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), reply, false)
	a.voiceGate("option 1") // group 0
	if a.clarifyState.reply == nil {
		t.Fatal("answers were sent with a group still open")
	}
	a.voiceGate("option 2") // group 1, multi: toggles only
	if a.clarifyState.reply == nil {
		t.Fatal("a multi-choice group sent the answers by itself")
	}
	a.voiceGate("submit")
	select {
	case ans := <-reply:
		if ans.Items[0].Chosen[0] != 0 || ans.Items[1].Chosen[0] != 1 {
			t.Fatalf("answers = %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("submit did not send")
	}
}

func TestSkipSkipsTheFirstOpenGroup(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.push(viewClarify)
	reply := make(chan clarify.Answers, 1)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), reply, false)
	a.voiceGate("skip")
	a.voiceGate("skip")
	select {
	case ans := <-reply:
		if !ans.Items[0].Skipped || !ans.Items[1].Skipped {
			t.Fatalf("answers = %+v", ans)
		}
	case <-time.After(time.Second):
		t.Fatal("skipping every group did not send")
	}
}

func TestPermissionKeywords(t *testing.T) {
	for said, want := range map[string]bool{"Approved": true, "deny": false} {
		a := gateApp(t, config.VoiceSettings{})
		a.push(viewPermissionAsk)
		reply := make(chan agent.PermissionAskReply, 1)
		a.permAskState = permissionAskViewState{reply: reply}
		if _, _, done := a.voiceGate(said); !done {
			t.Fatalf("%q was not consumed", said)
		}
		select {
		case r := <-reply:
			if r.Allow != want {
				t.Fatalf("%q allow = %v", said, r.Allow)
			}
		case <-time.After(time.Second):
			t.Fatalf("%q did not answer the ask", said)
		}
		if a.permAskState.reply != nil {
			t.Fatal("the ask is still open")
		}
	}
}

func TestOnlyKeywordsPassWhileAnAskIsOpen(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.voice.ready = false
	a.push(viewPermissionAsk)
	a.permAskState = permissionAskViewState{reply: make(chan agent.PermissionAskReply, 1)}
	text, cmd, done := a.voiceGate("please delete everything")
	if !done || cmd != nil || text != "" || len(a.voice.queue) != 0 {
		t.Fatal("speech during an ask reached the dictation path")
	}
	// "option 2" means nothing to a permission ask, and is not dictated either.
	if _, _, done := a.voiceGate("option 2"); !done {
		t.Fatal("a keyword that does not apply was dictated")
	}
	if a.permAskState.reply == nil {
		t.Fatal("an unrelated keyword answered the ask")
	}
}

func TestCommandsOffMakesEveryKeywordPlainSpeech(t *testing.T) {
	off := false
	a := gateApp(t, config.VoiceSettings{Commands: &off})
	a.cancel = func() { t.Fatal("stop ran with commands off") }
	if text, _, done := a.voiceGate("stop"); done || text != "stop" {
		t.Fatalf("got %q done=%v", text, done)
	}
	a.push(viewClarify)
	a.clarifyState = newClarifyState(sampleQuestionnaire(), make(chan clarify.Answers, 1), false)
	if a.voiceCommandReady() {
		t.Fatal("the microphone stayed open for keywords with commands off")
	}
}

func TestKeywordRemovesItsOwnLiveGuess(t *testing.T) {
	a := gateApp(t, config.VoiceSettings{})
	a.cancel = func() {}
	a.voicePartial("stop")
	if a.editor.Value() != "stop" {
		t.Fatalf("setup: composer %q", a.editor.Value())
	}
	a.voiceGate("stop")
	if a.editor.Value() != "" || a.voice.live.active {
		t.Fatalf("the keyword was left in the composer: %q", a.editor.Value())
	}
}
