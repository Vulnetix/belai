package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

const askArgs = `{"questions":[{"question":"Which database should the service use?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`

func askSession(t *testing.T, planMode, allowAsk bool) (*Session, func()) {
	t.Helper()
	srv := mockSecurityServer("AskUserQuestion", askArgs, "done")
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(tools.AskUserQuestion{}, &tools.Write{Root: root, MaxBytes: 1024}, tools.ExitPlanMode{}),
		Posture:       posture.Defaults(),
		Workdir:       root,
		PlanMode:      planMode,
		AllowPassLoop: true,
		AllowAsk:      allowAsk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess, srv.Close
}

// answer picks the first option of every group.
func answer(q clarify.Questionnaire) clarify.Answers {
	var a clarify.Answers
	for i := range q.Groups {
		a.Items = append(a.Items, clarify.Answer{GroupIndex: i, Chosen: []int{0}})
	}
	return a
}

// Agent mode asks at once: the answers are the tool result and the turn
// carries on to its reply.
func TestAskUserInlineInAgentMode(t *testing.T) {
	sess, done := askSession(t, false, true)
	defer done()
	var asked int
	var result string
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "set up storage"}, false, func(e Event) {
		switch e.Kind {
		case EventClarifyAskKind:
			asked++
			e.Reply <- answer(*e.Clarify)
		case EventToolResultKind:
			if e.ToolName == "AskUserQuestion" {
				result = e.ToolResult
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || !strings.Contains(result, "chose: Postgres") || res.Reply != "done" || res.Clarify != nil {
		t.Fatalf("asked=%d result=%q reply=%q", asked, result, res.Reply)
	}
}

// With nobody to ask, the model is told to proceed instead of waiting.
func TestAskUserUnavailableHeadless(t *testing.T) {
	sess, done := askSession(t, false, false)
	defer done()
	var result string
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "set up storage"}, false, func(e Event) {
		if e.Kind == EventClarifyAskKind {
			t.Fatal("asked with nobody to answer")
		}
		if e.Kind == EventToolResultKind && e.ToolName == "AskUserQuestion" {
			result = e.ToolResult
		}
	}); err != nil {
		t.Fatal(err)
	}
	if result != askUserUnavailable {
		t.Fatalf("result = %q", result)
	}
}

// Plan mode: asking ends planning, the answers start a new agent-mode turn
// with the full surface, and the same question is not put to the user again.
func TestAskUserInPlanModeHandsOffToAgentTurn(t *testing.T) {
	sess, done := askSession(t, true, true)
	defer done()
	var asked int
	var second string
	var handoff, planDuringSecond bool
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "plan the storage layer", ForceMode: modes.ModePlan}, false, func(e Event) {
		switch e.Kind {
		case EventClarifyAskKind:
			asked++
			e.Reply <- answer(*e.Clarify)
		case EventWarningKind:
			handoff = handoff || strings.Contains(e.Warning, "continuing in agent mode")
		case EventToolResultKind:
			if e.ToolName == "AskUserQuestion" && handoff {
				second = e.ToolResult
				planDuringSecond = sess.planMode
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}
	if !handoff || planDuringSecond {
		t.Fatalf("handoff=%v planDuringSecond=%v: the answers must run as an agent-mode turn", handoff, planDuringSecond)
	}
	if second != askUserRepeated {
		t.Fatalf("repeated question result = %q", second)
	}
	if res.Clarify != nil || res.Reply != "done" {
		t.Fatalf("result = %+v", res)
	}
	if !sess.planMode {
		t.Fatal("the session's plan mode was not restored after the handoff")
	}
}

func TestAskUserOfferedEverywhereButSubagents(t *testing.T) {
	reg := tools.Default(t.TempDir(), false)
	for name, r := range map[string]*tools.Registry{"agent": reg.WithoutPlanOnly(), "plan": reg.Plan(), "read-only": reg.ReadOnly()} {
		if _, ok := r.Find("AskUserQuestion"); !ok {
			t.Errorf("%s surface lacks AskUserQuestion", name)
		}
	}
	if !contains(strings.Join(planFinishTools, ","), "AskUserQuestion") {
		t.Fatal("final plan pass lacks AskUserQuestion")
	}
}

func TestAskedQuestionsAreRememberedAcrossSources(t *testing.T) {
	s := &Session{}
	q := clarify.Questionnaire{Groups: []clarify.Group{{Context: "Which database?"}, {Context: "Which port?"}}}
	s.markAsked(clarify.Questionnaire{Groups: []clarify.Group{{Context: "which database"}}})
	got := s.dropAskedBefore(q)
	if len(got.Groups) != 1 || got.Groups[0].Context != "Which port?" {
		t.Fatalf("groups = %+v", got.Groups)
	}
}
