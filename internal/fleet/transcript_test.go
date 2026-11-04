package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tools"
)

// A worker turn's session ends with how the turn went, never a blanket
// superseded: the recorded outcome first, then what stopped the item, then
// the turn's error, then the goal loop's stop reason.
func TestTurnEnd(t *testing.T) {
	cases := []struct {
		name      string
		res       run.Result
		err       error
		cause     error
		rec       tools.Outcome
		reason    string
		errSubstr string
	}{
		{"goal complete", run.Result{StopReason: run.StopComplete}, nil, nil, "", sessionsync.EndGoalComplete, ""},
		{"recorded success", run.Result{StopReason: run.StopDecided}, nil, nil, tools.OutcomeSuccess, sessionsync.EndGoalComplete, ""},
		{"recorded failure", run.Result{StopReason: run.StopDecided}, nil, nil, tools.OutcomeFailure, sessionsync.EndError, "recorded failure"},
		{"recorded blocked", run.Result{StopReason: run.StopDecided}, nil, nil, tools.OutcomeBlocked, sessionsync.EndGoalPaused, "cannot continue"},
		{"recorded outcome outranks the wall budget", run.Result{}, context.Canceled, errWallBudget, tools.OutcomeBlocked, sessionsync.EndGoalPaused, "cannot continue"},
		{"wall budget", run.Result{}, context.Canceled, errWallBudget, "", sessionsync.EndTimeout, "max_wall_per_item"},
		{"token budget", run.Result{}, context.Canceled, errTokenBudget, "", sessionsync.EndBudgetExceeded, "max_tokens_per_item"},
		{"cost budget", run.Result{}, context.Canceled, errCostBudget, "", sessionsync.EndBudgetExceeded, "max_cost_usd_per_item"},
		{"lease lost", run.Result{}, context.Canceled, errLeaseLost, "", sessionsync.EndInterrupted, "released elsewhere"},
		{"shutdown", run.Result{}, context.Canceled, context.Canceled, "", sessionsync.EndInterrupted, ""},
		{"rate limited", run.Result{}, &run.ProviderError{Status: http.StatusTooManyRequests}, nil, "", sessionsync.EndRateLimited, ""},
		{"auth", run.Result{}, &run.ProviderError{Status: http.StatusUnauthorized}, nil, "", sessionsync.EndAuthFailed, ""},
		{"plain error", run.Result{}, errors.New("disk\x1b[31m full"), nil, "", sessionsync.EndError, "disk"},
		{"asks withheld", run.Result{StopReason: run.StopIncomplete, AsksWithheld: []string{"Bash"}}, nil, nil, "", sessionsync.EndGoalPaused, "needs permission: Bash"},
		{"max passes", run.Result{StopReason: run.StopMaxPasses}, nil, nil, "", sessionsync.EndMaxTurns, ""},
		{"stalled", run.Result{StopReason: run.StopStalled}, nil, nil, "", sessionsync.EndGoalPaused, "goal stalled"},
		{"cancelled", run.Result{StopReason: run.StopCancelled}, nil, nil, "", sessionsync.EndInterrupted, ""},
		{"coordinated", run.Result{StopReason: run.StopCoordinated}, nil, nil, "", sessionsync.EndGoalComplete, ""},
		{"not a goal turn", run.Result{}, nil, nil, "", sessionsync.EndCompleted, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := turnEnd(c.res, c.err, c.cause, c.rec)
			if got.Reason != c.reason || !strings.Contains(got.Error, c.errSubstr) {
				t.Fatalf("turnEnd = %+v, want reason %q with error containing %q", got, c.reason, c.errSubstr)
			}
			if c.errSubstr == "" && c.err == nil && c.cause == nil && got.Error != "" {
				t.Fatalf("error %q on a clean end", got.Error)
			}
			if strings.ContainsRune(got.Error, 0x1b) {
				t.Fatalf("error carries a control character: %q", got.Error)
			}
		})
	}
}

// The worker's session reaches the website's end call with the turn's real
// outcome, recorded through the turn runner's transcript.
func TestWorkerEndsItsSessionWithTheTurnOutcome(t *testing.T) {
	for _, c := range []struct {
		name   string
		runner TurnRunner
		reason string
	}{
		{"complete", complete, sessionsync.EndGoalComplete},
		{"rate limited", func(context.Context, Turn) (run.Result, error) {
			return run.Result{}, &run.ProviderError{Status: http.StatusTooManyRequests}
		}, sessionsync.EndRateLimited},
		{"stalled", func(context.Context, Turn) (run.Result, error) {
			return run.Result{StopReason: run.StopStalled, Passes: 1}, nil
		}, sessionsync.EndGoalPaused},
	} {
		t.Run(c.name, func(t *testing.T) {
			store, reg := testEnv(t)
			if _, _, err := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}}, kanban.Provenance{}); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			ends := map[string]string{}
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if _, rest, ok := strings.Cut(r.URL.Path, "/sessions/"); ok && r.Method == http.MethodPost && strings.HasSuffix(rest, "/end") {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					ends[strings.TrimSuffix(rest, "/end")] = string(b)
					mu.Unlock()
				}
				rw.Header().Set("Content-Type", "application/json")
				rw.Write([]byte(`{"lastSeq":-1}`))
			}))
			defer srv.Close()
			client, err := sessionsync.NewClient(sessionsync.BaseURL(srv.URL), func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			w := newWorker(t, store, reg, builderProfile(), c.runner)
			w.Sessions = session.NewStoreAt(t.TempDir())
			w.Sync = client
			w.Runner = func(ctx context.Context, tt Turn) (run.Result, error) {
				tr := w.transcript(tt)
				tr.user(workPrompt)
				res, err := c.runner(ctx, tt)
				tr.finish(ctx, res, err)
				return res, err
			}
			_ = w.Run(context.Background())
			sid := w.Record.Session
			mu.Lock()
			defer mu.Unlock()
			body, ok := ends[sid]
			if !ok {
				t.Fatalf("session %s was not ended; ends %v", sid, ends)
			}
			var got sessionsync.EndInfo
			if err := json.Unmarshal([]byte(body), &got); err != nil || got.Reason != c.reason {
				t.Fatalf("end body %s, want reason %q (%v)", body, c.reason, fmt.Sprint(err))
			}
		})
	}
}

// The bug this guards: a worker's turn session that the next turn's session
// replaces ended superseded. It now ends with its own turn's outcome, and the
// next turn's outcome never lands on it.
func TestReplacedWorkerSessionKeepsItsTurnOutcome(t *testing.T) {
	var mu sync.Mutex
	put, ends := map[string]bool{}, map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if _, rest, ok := strings.Cut(r.URL.Path, "/sessions/"); ok {
			mu.Lock()
			switch {
			case r.Method == http.MethodPut && !strings.Contains(rest, "/"):
				put[rest] = true
			case r.Method == http.MethodPost && strings.HasSuffix(rest, "/end"):
				b, _ := io.ReadAll(r.Body)
				ends[strings.TrimSuffix(rest, "/end")] = string(b)
			}
			mu.Unlock()
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Write([]byte(`{"lastSeq":-1}`))
	}))
	defer srv.Close()
	client, err := sessionsync.NewClient(sessionsync.BaseURL(srv.URL), func() (string, error) { return "ApiKey org:hex", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Profile: builderProfile(), Repo: t.TempDir(), Sessions: session.NewStoreAt(t.TempDir()), Log: io.Discard}
	w.mirror = sessionsync.New(sessionsync.Options{Client: client, HostID: "11111111-1111-4111-8111-111111111111"})
	w.mirror.Start(context.Background())
	seen := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 500; i++ {
			mu.Lock()
			done := ok()
			mu.Unlock()
			if done {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s did not happen", what)
	}
	item := kanban.Item{ID: "item-1", Title: "do it"}
	first, second := session.MustID(), session.MustID()
	ctx := context.Background()

	tr := w.transcript(Turn{Item: item, Workdir: w.Repo, SessionID: first})
	tr.user(workPrompt)
	tr.finish(ctx, run.Result{StopReason: run.StopMaxPasses, Passes: 3}, nil)
	seen("first session registered", func() bool { return put[first] })

	tr = w.transcript(Turn{Item: item, Workdir: w.Repo, SessionID: second})
	tr.user(workPrompt)
	seen("first session ended", func() bool { return ends[first] != "" })
	tr.finish(ctx, run.Result{}, &run.ProviderError{Status: http.StatusTooManyRequests})
	seen("second session registered", func() bool { return put[second] })
	w.mirror.Close(5 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	for id, want := range map[string]string{first: sessionsync.EndMaxTurns, second: sessionsync.EndRateLimited} {
		var got sessionsync.EndInfo
		if err := json.Unmarshal([]byte(ends[id]), &got); err != nil || got.Reason != want {
			t.Errorf("session %s end body %q, want reason %q", id, ends[id], want)
		}
	}
}
