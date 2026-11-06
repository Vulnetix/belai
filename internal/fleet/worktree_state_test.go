package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/trustgate"
)

func trusted(t *testing.T, repo string) {
	t.Helper()
	if err := trustgate.Grant(repo, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRoundTripAndListing(t *testing.T) {
	ws, repo := updateWorkspace(t)
	if err := ws.SaveRecord(true); err != nil {
		t.Fatal(err)
	}
	recs, err := WorktreeRecords(repo)
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	r := recs[0]
	if r.ID != ws.ID() || r.Item != "K-3f9a2c" || r.Branch != ws.Branch || r.Base != ws.Base || r.BaseRef != "main" || !r.Kept || !r.Exists || r.Dir != ws.Dir {
		t.Fatalf("%+v (dir %q)", r, ws.Dir)
	}
	// The file is private and holds no path.
	dir, _, _ := recordDir(repo)
	path := filepath.Join(dir, r.ID+".json")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if raw, _ := os.ReadFile(path); strings.Contains(string(raw), ws.Dir) {
		t.Fatalf("the record names the directory: %s", raw)
	}
	// Saving again keeps the first time.
	if err := ws.SaveRecord(true); err != nil {
		t.Fatal(err)
	}
	if again, _ := WorktreeRecords(repo); len(again) != 1 || again[0].Created != r.Created {
		t.Fatalf("created moved: %+v", again)
	}
	// Removing the worktree drops the record.
	if err := ws.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left, _ := WorktreeRecords(repo); len(left) != 0 {
		t.Fatalf("record survived the removal: %+v", left)
	}
}

func TestRecordsRejectBadFiles(t *testing.T) {
	ws, repo := updateWorkspace(t)
	if err := ws.SaveRecord(false); err != nil {
		t.Fatal(err)
	}
	dir, _, _ := recordDir(repo)
	// Another id's record under this id, a path-shaped name, a symlink and junk.
	good, _ := os.ReadFile(filepath.Join(dir, ws.ID()+".json"))
	os.WriteFile(filepath.Join(dir, "K-aaaaaa-bbbbbb.json"), good, 0o600)
	os.WriteFile(filepath.Join(dir, "..evil.json"), good, 0o600)
	os.WriteFile(filepath.Join(dir, "K-cccccc-dddddd.json"), []byte("{not json"), 0o600)
	os.Symlink(filepath.Join(dir, ws.ID()+".json"), filepath.Join(dir, "K-eeeeee-ffffff.json"))
	recs, err := WorktreeRecords(repo)
	if err != nil || len(recs) != 1 || recs[0].ID != ws.ID() {
		t.Fatalf("listing trusted a bad file: %+v %v", recs, err)
	}
	// A record whose branch is outside belai/ is not valid.
	bad := WorktreeRecord{ID: ws.ID(), Item: "K-3f9a2c", Branch: "main", Base: ws.Base, Created: 1}
	if err := writeRecord(repo, bad); err == nil {
		t.Fatal("a record for the main branch was written")
	}
	for _, id := range []string{"", "../x", "K-123", ws.ID() + "/x"} {
		if _, err := worktreeDir(repo, id); err == nil {
			t.Errorf("id %q built a directory", id)
		}
	}
}

func TestOpenWorktreeChecksWhatItReads(t *testing.T) {
	ctx := context.Background()
	ws, repo := updateWorkspace(t)
	if err := ws.SaveRecord(false); err != nil {
		t.Fatal(err)
	}
	got, err := OpenWorktree(ctx, repo, ws.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != ws.Dir || got.Branch != ws.Branch || got.BaseRef != "main" || !got.Worktree {
		t.Fatalf("%+v", got)
	}
	if _, err := OpenWorktree(ctx, repo, "K-aaaaaa-bbbbbb"); err == nil {
		t.Error("opened a worktree with no record")
	}
	if _, err := OpenWorktree(ctx, repo, "../../etc"); err == nil {
		t.Error("opened a path")
	}
	// A record that names a branch the worktree is not on is refused.
	rec, _ := readRecord(repo, ws.ID())
	rec.Branch = "belai/K-3f9a2c/a9"
	if err := writeRecord(repo, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorktree(ctx, repo, ws.ID()); err == nil {
		t.Error("opened a worktree on another branch than its record")
	}
	// A directory that is gone is reported, not guessed.
	os.RemoveAll(ws.Dir)
	if _, err := OpenWorktree(ctx, repo, ws.ID()); err == nil {
		t.Error("opened a missing directory")
	}
}

func TestOpenWorktreeRefusesAGitFilePointingElsewhere(t *testing.T) {
	ctx := context.Background()
	ws, repo := updateWorkspace(t)
	ws.SaveRecord(false)
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(ws.Dir, ".git"), []byte("gitdir: "+elsewhere+"\n"), 0o644)
	if _, err := OpenWorktree(ctx, repo, ws.ID()); err == nil {
		t.Fatal("opened a worktree whose git file points outside the repository")
	}
}

func TestUpdateAndDropWorktreeThroughTheFacade(t *testing.T) {
	ctx := context.Background()
	ws, repo := updateWorkspace(t)
	trusted(t, repo)
	if err := ws.SaveRecord(true); err != nil {
		t.Fatal(err)
	}
	commitFile(t, repo, "theirs.txt", "theirs\n", "feat: theirs")
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))

	// Off by default.
	if _, err := UpdateWorktree(ctx, repo, ws.ID(), config.Settings{}, store); !errors.Is(err, ErrUpdateOff) {
		t.Fatalf("%v", err)
	}
	mode := "merge"
	s := config.Settings{Worktrees: &config.WorktreeSettings{UpdateMode: &mode}}
	res, err := UpdateWorktree(ctx, repo, ws.ID(), s, store)
	if err != nil || res.Brought != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if rec, _ := readRecord(repo, ws.ID()); rec.Base != gitIn(t, repo, "rev-parse", "main") {
		t.Fatalf("the record kept the old base: %q", rec.Base)
	}

	// Teardown runs on a drop, and a failing one keeps the worktree.
	marker := filepath.Join(t.TempDir(), "torn-down")
	failing := config.Settings{Worktrees: &config.WorktreeSettings{Teardown: [][]string{{"false"}}}}
	pol := posture.AllIgnore()
	err = DropWorktree(ctx, repo, ws.ID(), failing, pol, store)
	if he, ok := AsHookError(err); !ok || he.Phase != "teardown" {
		t.Fatalf("drop with a failing teardown: %v", err)
	}
	if !exists(ws.Dir) {
		t.Fatal("a failing teardown still removed the worktree")
	}
	passing := config.Settings{Worktrees: &config.WorktreeSettings{Teardown: [][]string{{"touch", marker}}}}
	if err := DropWorktree(ctx, repo, ws.ID(), passing, pol, store); err != nil {
		t.Fatal(err)
	}
	if !exists(marker) || exists(ws.Dir) {
		t.Fatalf("teardown ran %v, worktree present %v", exists(marker), exists(ws.Dir))
	}
	// The branch holds work, so it stays.
	if out := gitIn(t, repo, "branch", "--list", ws.Branch); !strings.Contains(out, ws.Branch) {
		t.Fatal("dropping the worktree deleted a branch with work on it")
	}
}

func TestWorktreeCommandsRefuseAnUntrustedDirectory(t *testing.T) {
	ctx := context.Background()
	ws, repo := updateWorkspace(t)
	ws.SaveRecord(false)
	mode := "merge"
	s := config.Settings{Worktrees: &config.WorktreeSettings{UpdateMode: &mode}}
	if _, err := UpdateWorktree(ctx, repo, ws.ID(), s, nil); err == nil || !strings.Contains(err.Error(), "not a trusted workspace") {
		t.Errorf("update: %v", err)
	}
	if err := DropWorktree(ctx, repo, ws.ID(), s, posture.AllIgnore(), nil); err == nil || !strings.Contains(err.Error(), "not a trusted workspace") {
		t.Errorf("drop: %v", err)
	}
	if !exists(ws.Dir) {
		t.Fatal("an untrusted drop removed the worktree")
	}
}

func TestAClaimedCardMakesItsWorktreeBusy(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	it, _, err := store.Add(kanban.ItemInput{Title: "a card", Labels: []string{"build"}}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	rec := WorktreeRecord{Item: it.Short()}
	if worktreeClaimed(store, rec) {
		t.Fatal("an unclaimed card was seen as claimed")
	}
	if _, err := store.Claim(kanban.ClaimRequest{Worker: "w1", Labels: []string{"build"}, Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !worktreeClaimed(store, rec) {
		t.Fatal("a claimed card was not seen as claimed")
	}
	if worktreeClaimed(nil, rec) || worktreeClaimed(store, WorktreeRecord{Item: "K-000000"}) {
		t.Fatal("a missing board or card was seen as claimed")
	}
}

func TestNewWorktreeCardIsLabelledAndHarnessWorded(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	it, dup, err := NewWorktreeCard(store, kanban.Provenance{}, "Fix \x1b[31mthe\x00 login <tools>", "build")
	if err != nil || dup {
		t.Fatal(err)
	}
	if strings.ContainsAny(it.Title, "\x1b\x00") || strings.Contains(it.Title, "<tools>") {
		t.Fatalf("title not cleaned: %q", it.Title)
	}
	if !contains(it.Labels, config.WorktreeLabel) || !contains(it.Labels, "build") || it.List != kanban.Backlog {
		t.Fatalf("%+v", it)
	}
	if strings.Contains(it.Body, "login") {
		t.Fatalf("user text reached the harness-worded body: %q", it.Body)
	}
	if _, _, err := NewWorktreeCard(store, kanban.Provenance{}, "  "); err == nil {
		t.Fatal("an empty title was filed")
	}
	if _, _, err := NewWorktreeCard(nil, kanban.Provenance{}, "x"); err == nil {
		t.Fatal("a nil board was written")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
