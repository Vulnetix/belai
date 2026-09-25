package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

// TestReadStreakNudgeRules pins when the mid-pass edit nudge fires: every
// readStreakNudgeAfter productive rounds that changed no file, reset by a
// change, never for an unproductive round, and only on a surface that may edit.
func TestReadStreakNudgeRules(t *testing.T) {
	run := func(s *Session, mode modes.Mode, rounds int, mutateAt int) []int {
		var streak, seen, mutations int
		var fired []int
		for i := 1; i <= rounds; i++ {
			if i == mutateAt {
				mutations++
			}
			if s.readStreakNudge(&streak, &seen, mutations, true, mode) != "" {
				fired = append(fired, i)
			}
		}
		return fired
	}

	s := &Session{}
	if got := run(s, modes.ModeGoal, 17, 0); len(got) != 2 || got[0] != readStreakNudgeAfter || got[1] != 2*readStreakNudgeAfter {
		t.Fatalf("goal nudges at %v, want rounds %d and %d", got, readStreakNudgeAfter, 2*readStreakNudgeAfter)
	}
	if got := run(s, modes.ModeGoal, readStreakNudgeAfter+2, 5); len(got) != 0 {
		t.Fatalf("a file change must reset the streak, nudged at %v", got)
	}
	var streak, seen int
	for i := 0; i < 3*readStreakNudgeAfter; i++ {
		if s.readStreakNudge(&streak, &seen, 0, false, modes.ModeGoal) != "" {
			t.Fatal("an unproductive round counted toward the streak")
		}
	}
	for name, sess := range map[string]*Session{
		"plan mode":        {planMode: true},
		"read-only agent":  {turnReadOnly: true},
		"explore subagent": {exploreSubagent: true},
		"report pass":      {reportOnly: true},
	} {
		if got := run(sess, modes.ModeGoal, 2*readStreakNudgeAfter, 0); len(got) != 0 {
			t.Fatalf("%s was nudged at %v", name, got)
		}
	}
	var gotGoal, gotAgent string
	streak, seen = readStreakNudgeAfter-1, 0
	gotGoal = s.readStreakNudge(&streak, &seen, 0, true, modes.ModeGoal)
	streak = readStreakNudgeAfter - 1
	gotAgent = s.readStreakNudge(&streak, &seen, 0, true, modes.ModeAgent)
	if gotGoal != readStreakDirective || gotAgent != agentEditNudge {
		t.Fatalf("goal = %q agent = %q, want the goal directive and the agent nudge", gotGoal, gotAgent)
	}
}

// TestGoalPassNudgesAReadOnlyStreak drives a real pass: a model that only
// reads is told, in a sealed directive, to start editing once the streak is
// reached.
func TestGoalPassNudgesAReadOnlyStreak(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600)

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		writeToolCallJSON(w, "Read", `{"path":"f.txt"}`)
	}))
	defer srv.Close()

	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:       posture.AllIgnore(),
		MaxIterations: readStreakNudgeAfter + 2,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := sess.pass(context.Background(), rolemanager.NewPipeline(nil), "system", []run.Turn{{Role: "user", Content: "fix it"}}, false, func(Event) {}, modes.ModeGoal)
	if err != nil {
		t.Fatal(err)
	}
	if !out.exhausted {
		t.Fatal("pass should spend its budget")
	}
	mu.Lock()
	defer mu.Unlock()
	marker := "Stop surveying"
	for i, b := range bodies {
		has := strings.Contains(b, marker)
		if i < readStreakNudgeAfter && has {
			t.Fatalf("request %d carried the nudge before the streak was reached", i+1)
		}
		if i == readStreakNudgeAfter && !has {
			t.Fatalf("request %d did not carry the nudge", i+1)
		}
	}
}
