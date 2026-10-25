package fleet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

func sandboxFile(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "attribution")
	if err := os.WriteFile(p, []byte("Co-authored-by: Ada Lovelace <ada@example.com>\nSigned-off-by: Pix [bot] <security@vulnetix.com>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIX_ATTRIBUTION_FILE", p)
}

func commitMessages(t *testing.T, dir, base string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--format=%B%x00", base+"..HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v %s", err, out)
	}
	var msgs []string
	for _, m := range strings.Split(string(out), "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			msgs = append(msgs, m)
		}
	}

	return msgs
}

func TestWorkspaceCommitCarriesTheSession(t *testing.T) {
	testEnv(t)
	sandboxFile(t)
	repo := gitRepo(t)
	ctx := context.Background()
	ws, err := PrepareWorktree(ctx, repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-000000000000", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ws.Session = "ses_77"
	os.WriteFile(filepath.Join(ws.Dir, "new.txt"), []byte("x\n"), 0o644)
	if n, err := ws.Commit(ctx, "belai: test"); err != nil || n != 1 {
		t.Fatalf("commit: %d %v", n, err)
	}
	msgs := commitMessages(t, ws.Dir, ws.Base)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Signed-off-by: Pix [bot] <security@vulnetix.com>") ||
		!strings.Contains(msgs[0], "Session: https://vulnetix.com/resolve/factory-session?id=ses_77") {
		t.Fatalf("messages %q", msgs)
	}
}

func TestEnsureAttributionRewritesOnlyWhatLacksIt(t *testing.T) {
	testEnv(t)
	repo := gitRepo(t)
	ctx := context.Background()
	ws, err := PrepareWorktree(ctx, repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-000000000001", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Two commits made with no attribution file in view, as a repository with its own hooks path would make them.
	for _, name := range []string{"a.txt", "b.txt"} {
		os.WriteFile(filepath.Join(ws.Dir, name), []byte(name+"\n"), 0o644)
		if n, err := ws.Commit(ctx, "feat: add "+name); err != nil || n != 1 {
			t.Fatalf("commit %s: %d %v", name, n, err)
		}
	}
	sandboxFile(t)
	ws.Session = "ses_5"
	ws.ensureAttribution(ctx)

	msgs := commitMessages(t, ws.Dir, ws.Base)
	if len(msgs) != 2 {
		t.Fatalf("both commits stay: %q", msgs)
	}
	for _, m := range msgs {
		if strings.Count(m, "Signed-off-by: Pix [bot]") != 1 || !strings.Contains(m, "Co-authored-by: Ada Lovelace <ada@example.com>") {
			t.Fatalf("message lacks its trailers: %q", m)
		}
	}
	before := commitMessages(t, ws.Dir, ws.Base)
	ws.ensureAttribution(ctx)
	if after := commitMessages(t, ws.Dir, ws.Base); strings.Join(after, "|") != strings.Join(before, "|") {
		t.Fatalf("a second pass changed history: %q vs %q", after, before)
	}
}

func TestEnsureAttributionIsInertOutsideASandbox(t *testing.T) {
	testEnv(t)
	t.Setenv("PIX_ATTRIBUTION_FILE", filepath.Join(t.TempDir(), "absent"))
	repo := gitRepo(t)
	ctx := context.Background()
	ws, err := PrepareWorktree(ctx, repo, kanban.Item{ID: "3f9a2c00-0000-4000-8000-000000000002", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws.Dir, "a.txt"), []byte("a\n"), 0o644)
	if _, err := ws.Commit(ctx, "feat: add a"); err != nil {
		t.Fatal(err)
	}
	ws.ensureAttribution(ctx)
	if msgs := commitMessages(t, ws.Dir, ws.Base); len(msgs) != 1 || strings.Contains(msgs[0], "Signed-off-by") {
		t.Fatalf("a developer's commit is not signed off by a bot: %q", msgs)
	}
}
