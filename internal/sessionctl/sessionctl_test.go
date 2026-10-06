package sessionctl

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func base() State {
	return FromSettings(config.Settings{}, "agent", "anthropic", "claude-x", "")
}

func env() Env {
	return Env{
		CheckModel: func(p, m, e string) string {
			if p == "anthropic" || p == "openai" {
				return ""
			}
			return "this host has no credentials for provider " + p
		},
		Efforts: func(string, string) []string { return []string{"low", "medium", "high"} },
		Swap: func(st State) (string, string, bool) {
			if st.Model == "fast" {
				return "anthropic", "claude-x", true
			}
			return "anthropic", "fast", true
		},
	}
}

func TestDefaultsFromSettings(t *testing.T) {
	st := base()
	if st.Mode != "agent" || !st.Guardrails || !st.Ask || st.Caveman || st.AutoCommit {
		t.Fatalf("defaults: %+v", st)
	}
	if st.Reasoning != "auto" || st.Tools != "auto" || st.Decisions != "hidden" || st.TestsPostEnd != "off" || !st.LSP {
		t.Fatalf("display defaults: %+v", st)
	}
}

func TestKeysMatchTheTUI(t *testing.T) {
	cases := []struct {
		key, control, summary string
	}{
		{"shift+tab", CtlMode, "mode: plan"},
		{"f5", CtlMode, "mode: plan"},
		{"f2", CtlCaveman, "caveman: on"},
		{"f4", CtlAsk, "ask: off"},
		{"f6", CtlEffort, "reasoning effort: low"},
		{"ctrl+r", CtlReasoning, "reasoning display: shown"},
		{"ctrl+t", CtlTools, "tool-call display: all"},
		{"ctrl+q", CtlModel, "model: anthropic/fast"},
	}
	for _, c := range cases {
		ch, err := ParseKey(c.key, base(), env())
		if err != nil {
			t.Fatalf("%s: %v", c.key, err)
		}
		if ch.Control != c.control || ch.Summary != c.summary {
			t.Fatalf("%s: got %s %q", c.key, ch.Control, ch.Summary)
		}
	}
	for _, k := range Keys() {
		if _, err := ParseKey(k, base(), env()); errors.Is(err, ErrUnknown) {
			t.Fatalf("catalogue key %s has no binding", k)
		}
	}
}

func TestModeCyclesLikeTheTUI(t *testing.T) {
	st := base()
	var got []string
	for range 5 {
		ch, err := ParseKey("shift+tab", st, env())
		if err != nil {
			t.Fatal(err)
		}
		st = ch.State
		got = append(got, st.Mode)
	}
	if strings.Join(got, ",") != "plan,goal,code,auto,agent" {
		t.Fatalf("cycle = %v", got)
	}
}

func TestGuardrailsOffNeedsTheHost(t *testing.T) {
	if _, err := ParseKey("f3", base(), env()); err == nil {
		t.Fatal("guardrails off allowed without the host flag")
	}
	e := env()
	e.AllowGuardrailsOff = true
	ch, err := ParseKey("f3", base(), e)
	if err != nil || ch.State.Guardrails || !ch.Rebuild {
		t.Fatalf("with the flag: %+v %v", ch, err)
	}
	// Turning them back on never needs the flag.
	ch, err = Parse("/guardrails on", ch.State, env())
	if err != nil || !ch.State.Guardrails {
		t.Fatalf("on: %v", err)
	}
}

func TestParseRefusesWhatItDoesNotKnow(t *testing.T) {
	bad := []string{
		"/help", "/mode yolo", "/caveman maybe", "/model", "/model evil x",
		"/tests command rm", "/tests post_end always", "/lsp on cobol",
		"/jev job nope on", "/jev threshold allow_at 0.9", "/jev threshold nope 0.1",
		"/effort extreme", "!ls", "",
	}
	for _, l := range bad {
		if _, err := Parse(l, base(), env()); err == nil {
			t.Fatalf("%q accepted", l)
		}
	}
	if IsCommand("/help") || !IsCommand("/caveman on") || IsCommand("hello") {
		t.Fatal("IsCommand")
	}
}

func TestStateIsNotShared(t *testing.T) {
	st := base()
	ch, err := Parse("/jev job bash_swap off", st, env())
	if err != nil {
		t.Fatal(err)
	}
	if st.JevJobs != nil {
		t.Fatal("parse mutated the input state")
	}
	if ch.State.JevJobs["bash_swap"] {
		t.Fatal("job not off")
	}
}

func TestApplyOverlaysSessionValues(t *testing.T) {
	st := base()
	for _, l := range []string{
		"/caveman on", "/autocommit on", "/reasoning hidden", "/tools edits",
		"/decisions all", "/tests post_end goal", "/tests on_fail diagnose",
		"/lsp off", "/lsp on go", "/jev job bash_swap off", "/jev threshold simple_at 0.85",
	} {
		ch, err := Parse(l, st, env())
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		st = ch.State
	}
	s := st.Apply(config.Settings{})
	switch {
	case !s.CavemanEnabled(), !s.AutoCommitPerTaskEnabled(), s.ReasoningVisible():
		t.Fatal("toggles")
	case s.ToolCallsVisible(), !s.EditsVisible(), s.InternalWorkLevel() != "all":
		t.Fatal("display")
	case s.TestsPostEnd() != "goal", s.TestsOnFail() != "diagnose":
		t.Fatal("tests")
	case s.LSPEnabled():
		t.Fatal("lsp")
	case s.JevJobSet(config.JevBashSwap), s.JevThresholds().SimpleAt != 0.85:
		t.Fatal("jev")
	}
	if on, explicit := s.LSPLanguageEnabled("go"); !on || !explicit {
		t.Fatal("lsp language")
	}
	if err := config.ValidateJev(s); err != nil {
		t.Fatal(err)
	}
}

func TestModelChecksTheHost(t *testing.T) {
	if _, err := Parse("/model mistral large", base(), env()); err == nil {
		t.Fatal("unknown provider accepted")
	}
	ch, err := Parse("/model OpenAI gpt-5 high", base(), env())
	if err != nil || ch.State.Provider != "openai" || ch.State.Model != "gpt-5" || ch.State.Effort != "high" {
		t.Fatalf("%+v %v", ch.State, err)
	}
	if _, err := Parse("/model openai gpt-5", base(), Env{}); err == nil {
		t.Fatal("model change without a checker")
	}
}
