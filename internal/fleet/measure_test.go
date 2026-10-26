package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/turnlog"
)

// sessionLog opens a transcript log over a temp store and reads it back.
func sessionLog(t *testing.T) (*turnlog.Log, string, func() []session.Entry) {
	t.Helper()
	st := session.NewStoreAt(t.TempDir())
	key := session.Key("proj-1")
	w, err := session.NewWriter(st, key, session.MustID(), session.Meta{Cwd: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	return turnlog.New(w), w.ID(), func() []session.Entry {
		entries, err := st.ReadFrom(key, w.ID())
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
}

func entriesNamed(entries []session.Entry, typ string) []session.Entry {
	var out []session.Entry
	for _, e := range entries {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestChecksOfTheClaimLandInItsSession(t *testing.T) {
	log, id, read := sessionLog(t)
	w := &Worker{}
	w.Record.Session = id
	w.sessLog.Store(log)

	plan := testrun.Plan{Names: []string{"gate:g1", "go", "denied", "lint"}, Argvs: [][]string{
		{"go", "test", "-run", "^TestX$", "./p"}, {"go", "test", "./..."}, {"npm", "test"}, {"golangci-lint", "run"},
	}}
	results := []testrun.Result{
		{Command: plan.Argvs[0], Status: testrun.Passed},
		{Command: plan.Argvs[1], Status: testrun.Failed, ExitCode: 1},
		{Command: plan.Argvs[2], Status: testrun.Denied},
		{Command: plan.Argvs[3], Status: testrun.Passed},
	}
	w.recordChecks(sessionmeasure.KindTest, plan, results)

	got := entriesNamed(read(), "check_run")
	if len(got) != 3 {
		t.Fatalf("%d check_run entries, want 3 (the refused run is not a check): %+v", len(got), got)
	}
	want := []struct {
		kind, program string
		ok            bool
	}{{"gate", "go", true}, {"test", "go", false}, {"test", "golangci-lint", true}}
	for i, e := range got {
		if e.Meta["kind"] != want[i].kind || e.Meta["program"] != want[i].program || e.Meta["ok"] != want[i].ok {
			t.Errorf("entry %d meta = %v, want %+v", i, e.Meta, want[i])
		}
	}
}

func TestAnotherClaimsSessionIsNotWrittenTo(t *testing.T) {
	log, _, read := sessionLog(t)
	w := &Worker{}
	w.Record.Session = "a-later-session"
	w.sessLog.Store(log)
	plan := testrun.Plan{Names: []string{"go"}, Argvs: [][]string{{"go", "test"}}}
	w.recordChecks(sessionmeasure.KindTest, plan, []testrun.Result{{Command: []string{"go", "test"}, Status: testrun.Passed}})
	w.recordCommit(sessionmeasure.Commit{SHA: "0123456789abcdef0123456789abcdef01234567"})
	if n := len(entriesNamed(read(), "check_run")) + len(entriesNamed(read(), "commit")); n != 0 {
		t.Fatalf("%d entries written to a session the claim is not on", n)
	}
	// No log at all is fine too.
	(&Worker{}).recordChecks(sessionmeasure.KindTest, plan, nil)
}

func TestWorkspaceCommitIsRecorded(t *testing.T) {
	testEnv(t)
	repo := gitRepo(t)
	ctx := context.Background()
	ws, err := PrepareWorktree(ctx, repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-0000000000aa", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []sessionmeasure.Commit
	ws.OnCommit = func(c sessionmeasure.Commit) { got = append(got, c) }

	// Nothing to commit: nothing recorded.
	if n, err := ws.Commit(ctx, "belai: nothing"); err != nil || n != 0 || len(got) != 0 {
		t.Fatalf("empty commit: %d %v %d", n, err, len(got))
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ws.Commit(ctx, "belai: add new"); err != nil || n != 1 {
		t.Fatalf("commit: %d %v", n, err)
	}
	head, err := ws.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d commits recorded, want 1", len(got))
	}
	c := got[0]
	if c.SHA != head || c.Branch != ws.Branch || c.AuthorEmail == "" || c.CommitterEmail == "" {
		t.Fatalf("commit = %+v, head %s branch %s", c, head, ws.Branch)
	}

	// The worker writes it to the claim's session.
	log, id, read := sessionLog(t)
	w := &Worker{}
	w.Record.Session = id
	w.sessLog.Store(log)
	w.recordCommit(c)
	entries := entriesNamed(read(), "commit")
	if len(entries) != 1 || entries[0].Meta["sha"] != head || entries[0].Meta["branch"] != ws.Branch {
		t.Fatalf("commit entries = %+v", entries)
	}
}
