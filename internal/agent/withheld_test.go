package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// A provider rejection body is kilobytes of JSON and a server-side stack
// trace. Promoting it verbatim floods both the model's context and the
// transcript with text neither can act on, so the placeholder clips it.
func TestClassifierWithheldClipsLongProviderBody(t *testing.T) {
	body := `provider returned 400: {"errors":[{"message":"AiError: AiError: {\"object\":\"error\",` +
		"\n" + strings.Repeat("validation error detail ", 100) + "\n}]}"
	got := classifierWithheld("Bash", errors.New(body))

	if !strings.HasPrefix(got, `tool result withheld: classifier error for "Bash": `) {
		t.Fatalf("missing prefix: %q", got)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("placeholder spans multiple lines: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > 256 {
		t.Fatalf("placeholder is %d runes, want <= 256: %q", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipped placeholder should end in an ellipsis: %q", got)
	}
	if !strings.Contains(got, "provider returned 400") {
		t.Fatalf("placeholder dropped the actionable head of the error: %q", got)
	}
}

// A short error is already actionable and survives intact.
func TestClassifierWithheldKeepsShortError(t *testing.T) {
	got := classifierWithheld("Read", errors.New("request: connection refused"))
	want := `tool result withheld: classifier error for "Read": request: connection refused`
	if got != want {
		t.Fatalf("classifierWithheld = %q, want %q", got, want)
	}
}

// withheldPassServer is a minimal mock that makes the main model return a
// valid mutating tool call that the permission gate withholds (AskDisabled is
// false, so a Write without a TTY is refused). The pass therefore sees a
// withheld result without any classifier round trip. Two iterations in a row
// trigger the mode-aware withheld directive; a third exhausts the pass.
func withheldPassServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeToolCallJSON(w, "Write", `{"file_path":"f.txt","content":"x"}`)
	}))
	return srv
}

func newWithheldPassSession(t *testing.T, srv *httptest.Server, maxIter int) *Session {
	t.Helper()
	root := t.TempDir()
	cwd := tools.NewCwd(root)
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "test-key", Model: "test"}
	sess, err := NewSession(Options{
		Cfg:           cfg,
		Client:        srv.Client(),
		Workdir:       root,
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024, Cwd: cwd}, &tools.Write{Root: root, Cwd: cwd}),
		Posture:       posture.Defaults(),
		MaxIterations: maxIter,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// The repair directive must depend on mode: agent/goal mode never hears the
// plan-mode surrender, and plan mode keeps its original wording.
func TestWithheldDirectiveIsModeAware(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   modes.Mode
		want   string
		reject string
	}{
		{name: "goal", mode: modes.ModeGoal, want: withheldRepairDirective, reject: "Writes are unavailable in plan mode"},
		{name: "agent", mode: modes.ModeAgent, want: withheldRepairDirective, reject: "Writes are unavailable in plan mode"},
		{name: "plan", mode: modes.ModePlan, want: "Writes are unavailable in plan mode. Put the plan in your reply text, then call ExitPlanMode to finish.", reject: withheldRepairDirective},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := withheldPassServer(t)
			defer srv.Close()
			sess := newWithheldPassSession(t, srv, 2)

			pipe := rolemanager.NewPipeline(run.NewClassifier(sess.cfg, sess.client))
			_, turns, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {}, tc.mode)
			if err != nil {
				t.Fatalf("pass: %v", err)
			}
			found := false
			for _, turn := range turns {
				if strings.Contains(turn.Content, tc.want) {
					found = true
				}
				if strings.Contains(turn.Content, tc.reject) {
					t.Fatalf("mode %s got the wrong directive: %q", tc.mode, turn.Content)
				}
			}
			if !found {
				t.Fatalf("mode %s did not inject the repair directive", tc.mode)
			}
		})
	}
}

// Three withheld iterations exhaust the pass exactly as before: the mode-aware
// directive must not change the exhaustion boundary.
func TestWithheldExhaustionStillAtThree(t *testing.T) {
	srv := withheldPassServer(t)
	defer srv.Close()
	sess := newWithheldPassSession(t, srv, 3)

	pipe := rolemanager.NewPipeline(run.NewClassifier(sess.cfg, sess.client))
	out, _, err := sess.pass(context.Background(), pipe, "", nil, false, func(Event) {}, modes.ModeGoal)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if !out.exhausted || out.withheld != 3 {
		t.Fatalf("out = %+v, want exhausted with withheld==3", out)
	}
}
