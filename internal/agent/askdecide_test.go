package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// fakeDecider answers every proposition with judge and every choice with the
// weights of its question (by group context), recording what it was asked.
type fakeDecider struct {
	mu       sync.Mutex
	th       float64
	on       bool
	judge    float64
	err      error
	weights  map[string][]float64 // by question text
	judged   []string
	choices  []string
	contexts []string
}

func (f *fakeDecider) AskPolicy() (float64, bool) { return f.th, f.on }

func (f *fakeDecider) Judge(_ context.Context, prop, ctx string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.judged = append(f.judged, prop)
	f.contexts = append(f.contexts, ctx)
	return f.judge, f.err
}

func (f *fakeDecider) Choose(_ context.Context, q, ctx string, options []string, _ map[string]string) ([]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.choices = append(f.choices, q)
	if f.err != nil {
		return nil, f.err
	}
	w, ok := f.weights[q]
	if !ok {
		return nil, errors.New("no weights")
	}
	return w, nil
}

func writeSessionWith(t *testing.T, d AskDecider) (*Session, string, func()) {
	t.Helper()
	root := t.TempDir()
	srv := mockSecurityServer("Write", writeArgs(), "done")
	sess, err := NewSession(Options{
		Cfg:        run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:     srv.Client(),
		Registry:   tools.NewRegistry(&tools.Write{Root: root}),
		Posture:    posture.Defaults(),
		Workdir:    root,
		AllowAsk:   true,
		AskDecider: d,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess, root, srv.Close
}

// runWrite runs one Write turn and reports whether the user was asked, what was
// decided and what the tool result said. userAllow answers the ask when it comes.
func runWrite(t *testing.T, sess *Session, userAllow bool) (asked bool, decided *AskDecided, result string) {
	t.Helper()
	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "write a file"}, false, func(e Event) {
		switch e.Kind {
		case EventPermissionAskKind:
			asked = true
			e.AskReply <- PermissionAskReply{Allow: userAllow}
		case EventAskDecidedKind:
			decided = e.Decided
		case EventToolResultKind:
			if e.ToolName == "Write" {
				result = e.ToolResult
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

// A confident yes lets the call run without asking the user.
func TestAskDecisionAllowsAConfidentCall(t *testing.T) {
	d := &fakeDecider{th: 0.9, on: true, judge: 0.97}
	sess, root, done := writeSessionWith(t, d)
	defer done()
	asked, decided, _ := runWrite(t, sess, false)
	if asked {
		t.Fatal("a confident decision must not ask the user")
	}
	if decided == nil || decided.Kind != AskKindPermission || decided.Outcome != "allow" || decided.Name != "Write" || decided.Confidence < 0.96 {
		t.Fatalf("decided = %+v", decided)
	}
	if b, err := os.ReadFile(filepath.Join(root, "x.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("file not written: %v %q", err, b)
	}
	if len(d.judged) != 1 || !strings.Contains(d.judged[0], `"Write"`) || strings.Contains(d.judged[0], "hello") {
		t.Fatalf("the model must see the tool and target, never the arguments: %q", d.judged)
	}
	if len(d.contexts) != 1 || !strings.Contains(d.contexts[0], "write a file") {
		t.Fatalf("the model must see the user's request: %q", d.contexts)
	}
}

// A confident no withholds the call without asking, and says who decided.
func TestAskDecisionDeniesAConfidentNo(t *testing.T) {
	d := &fakeDecider{th: 0.9, on: true, judge: 0.03}
	sess, root, done := writeSessionWith(t, d)
	defer done()
	asked, decided, result := runWrite(t, sess, true)
	if asked || decided == nil || decided.Outcome != "deny" {
		t.Fatalf("asked=%v decided=%+v", asked, decided)
	}
	if !strings.Contains(result, "permission denied by the decision model") {
		t.Fatalf("result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("the file must not exist: %v", err)
	}
}

// Below the threshold, on a failed decision, or with the feature off, the user
// is asked exactly as before and their answer decides.
func TestAskDecisionFallsThroughToTheUser(t *testing.T) {
	cases := map[string]*fakeDecider{
		"below threshold": {th: 0.9, on: true, judge: 0.7},
		"error":           {th: 0.9, on: true, err: errors.New("down")},
		"off":             {th: 0.9, on: false, judge: 0.99},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			sess, root, done := writeSessionWith(t, d)
			defer done()
			asked, decided, _ := runWrite(t, sess, true)
			if !asked || decided != nil {
				t.Fatalf("asked=%v decided=%+v", asked, decided)
			}
			if _, err := os.Stat(filepath.Join(root, "x.txt")); err != nil {
				t.Fatalf("the user allowed it: %v", err)
			}
		})
	}
}

const twoGroupArgs = `{"questions":[` +
	`{"question":"Which database should the service use?","options":[{"label":"Postgres"},{"label":"SQLite"}]},` +
	`{"question":"Which cache should it use?","options":[{"label":"Redis"},{"label":"None"}]}]}`

func twoGroupSession(t *testing.T, d AskDecider) (*Session, func()) {
	t.Helper()
	srv := mockSecurityServer("AskUserQuestion", twoGroupArgs, "done")
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:        run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:     srv.Client(),
		Registry:   tools.NewRegistry(tools.AskUserQuestion{}),
		Posture:    posture.Defaults(),
		Workdir:    root,
		AllowAsk:   true,
		AskDecider: d,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess, srv.Close
}

func runAsk(t *testing.T, sess *Session, pick int) (asked []clarify.Questionnaire, decided *AskDecided, result string) {
	t.Helper()
	_, err := sess.run(context.Background(), nil, TurnInput{Prompt: "set up storage"}, false, func(e Event) {
		switch e.Kind {
		case EventClarifyAskKind:
			asked = append(asked, *e.Clarify)
			var a clarify.Answers
			for i := range e.Clarify.Groups {
				a.Items = append(a.Items, clarify.Answer{GroupIndex: i, Chosen: []int{pick}})
			}
			e.Reply <- a
		case EventAskDecidedKind:
			decided = e.Decided
		case EventToolResultKind:
			if e.ToolName == "AskUserQuestion" {
				result = e.ToolResult
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

// When every group is confident the user is not asked and the model is told the
// decision model chose.
func TestAskDecisionAnswersEveryConfidentGroup(t *testing.T) {
	d := &fakeDecider{th: 0.9, on: true, weights: map[string][]float64{
		"Which database should the service use?": {0.95, 0.05},
		"Which cache should it use?":             {0.02, 0.98},
	}}
	sess, done := twoGroupSession(t, d)
	defer done()
	asked, decided, result := runAsk(t, sess, 0)
	if len(asked) != 0 {
		t.Fatalf("the user must not be asked: %+v", asked)
	}
	if decided == nil || decided.Kind != AskKindClarify || decided.Decided != 2 || decided.Groups != 2 {
		t.Fatalf("decided = %+v", decided)
	}
	for _, want := range []string{"decided by the decision model", "chose: Postgres", "chose: None"} {
		if !strings.Contains(result, want) {
			t.Fatalf("result lacks %q: %q", want, result)
		}
	}
	if strings.Contains(result, "Clarifications from the user") {
		t.Fatalf("a decided answer must not be presented as the user's: %q", result)
	}
}

// Only the groups below the threshold go to the user, and the answers merge back
// under their own group numbers.
func TestAskDecisionLeavesUnconfidentGroupsToTheUser(t *testing.T) {
	d := &fakeDecider{th: 0.9, on: true, weights: map[string][]float64{
		"Which database should the service use?": {0.95, 0.05},
		"Which cache should it use?":             {0.55, 0.45},
	}}
	sess, done := twoGroupSession(t, d)
	defer done()
	asked, decided, result := runAsk(t, sess, 1)
	if len(asked) != 1 || len(asked[0].Groups) != 1 || !strings.Contains(asked[0].Groups[0].Context, "cache") {
		t.Fatalf("only the cache question may reach the user: %+v", asked)
	}
	if decided == nil || decided.Decided != 1 || decided.Groups != 2 {
		t.Fatalf("decided = %+v", decided)
	}
	// Group 1 was decided (Postgres); group 2 was answered by the user (second option).
	for _, want := range []string{"chose: Postgres", "chose: None", "decided by the decision model (confidence 0.95)"} {
		if !strings.Contains(result, want) {
			t.Fatalf("result lacks %q: %q", want, result)
		}
	}
	if !strings.Contains(result, "Clarifications from the user") {
		t.Fatalf("a mixed answer still holds the user's: %q", result)
	}
}

// A backend that cannot answer asks the user about every group.
func TestAskDecisionFailureAsksEverything(t *testing.T) {
	sess, done := twoGroupSession(t, &fakeDecider{th: 0.9, on: true, err: errors.New("down")})
	defer done()
	asked, decided, _ := runAsk(t, sess, 0)
	if len(asked) != 1 || len(asked[0].Groups) != 2 || decided != nil {
		t.Fatalf("asked=%+v decided=%+v", asked, decided)
	}
}
