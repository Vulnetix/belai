package fleet

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/mergefix"
	"github.com/vulnetix/belai/internal/run"
)

// ghShim writes a `gh` on a PATH-front directory whose case script answers
// the package's publish-path calls, so forge.For sees an installed gh and the
// github provider's calls reach the test. An unlisted command exits 1.
func ghShim(t *testing.T, cases string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" + cases + "\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestForgeOriginOnTheWorktree covers ForgeOrigin on both branches: true for a
// GitHub origin, false when there is none, and false outside a worktree.
func TestForgeOriginOnTheWorktree(t *testing.T) {
	testEnv(t)
	ws, repo := githubWorktree(t)
	if !ws.ForgeOrigin(context.Background()) {
		t.Fatal("a GitHub origin was not recognised")
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "remove", "origin").CombinedOutput(); err != nil {
		t.Fatalf("remote remove: %v %s", err, out)
	}
	if ws.ForgeOrigin(context.Background()) {
		t.Fatal("a repository without origin was recognised as a forge")
	}
	if (&Workspace{}).ForgeOrigin(context.Background()) {
		t.Fatal("a plain directory was recognised as a worktree")
	}
}

// TestPublishBranchReportsNothingToPublish covers the publish gate on a branch
// that holds no commit of its own: the error names the base and the command to
// check ancestry with. The worktree has a GitHub origin, so the gate about an
// empty branch fires, not the one about a missing origin.
func TestPublishBranchReportsNothingToPublish(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	other, err := PrepareWorktree(context.Background(), ws.repo, kanban.Item{ID: "8f9a2c00-0000-4000-8000-000000000002", Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Remove(context.Background())
	_, err = other.PublishBranch(context.Background(), "t", "b")
	if err == nil || !strings.Contains(err.Error(), "no changes beyond its base") || !strings.Contains(err.Error(), "merge-base") {
		t.Fatalf("publish a clean branch: %v", err)
	}
}

// TestPublishBranchOpensADraftPullRequest covers the full publish: the branch
// is pushed (the runner records the push so no network runs) and the shim
// opens the draft pull request. This is the call a worker's publish-at-done
// makes, at worktree.go:737.
func TestPublishBranchOpensADraftPullRequest(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	ghShim(t, `"pr view")
    exit 1 ;;
  "pr create")
    echo "https://github.com/acme/app/pull/9" ;;`)
	var pushArgs []string
	real := ws.run
	ws.run = func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if slices.Contains(argv, "push") {
			pushArgs = argv
			return []byte("branch pushed\n"), nil
		}
		return real(ctx, dir, argv...)
	}
	url, err := ws.PublishBranch(context.Background(), "t", "b")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if url != "https://github.com/acme/app/pull/9" {
		t.Fatalf("url %q", url)
	}
	if ref := "refs/heads/" + ws.Branch; !slices.Contains(pushArgs, ref+":"+ref) {
		t.Fatalf("push args %v", pushArgs)
	}
	// A second publish reuses the open draft instead of opening another.
	url2, err := ws.PublishBranch(context.Background(), "t", "b")
	if err != nil || url2 != url {
		t.Fatalf("republish %q %v", url2, err)
	}
}

// TestPushBranchSkipsAPushedBranch covers PushBranch on a branch whose work is
// already on origin: the up-to-date push counts as landed.
func TestPushBranchSkipsAPushedBranch(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	head, err := git(context.Background(), ws.run, ws.Dir, "rev-parse", "refs/heads/"+ws.Branch)
	if err != nil {
		t.Fatal(err)
	}
	real := ws.run
	pushes := 0
	ws.run = upToDateRunner(real, head, &pushes)
	if err := ws.PushBranch(context.Background()); err != nil {
		t.Fatalf("push an up-to-date branch: %v", err)
	}
	if pushes != 1 {
		t.Fatalf("pushes = %d", pushes)
	}
}

// TestCardPRsListsTheItemsOpenPullRequests covers CardPRs through the real
// provider path (forgeAuthed to gh pr list) and its guard on a branch with no
// card prefix. worktree.go:913.
func TestCardPRsListsTheItemsOpenPullRequests(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	ghShim(t, `"pr list")
    echo '[{"number":11,"title":"first","state":"OPEN","url":"https://github.com/acme/app/pull/11","isDraft":true,"headRefName":"`+ws.Branch+`"},{"number":12,"title":"other","state":"OPEN","url":"https://github.com/acme/app/pull/12","isDraft":false,"headRefName":"someones-branch"}]' ;;`)
	prs, err := ws.CardPRs(context.Background())
	if err != nil {
		t.Fatalf("card PRs: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 11 || prs[0].Branch != ws.Branch {
		t.Fatalf("prs = %+v", prs)
	}
	// A branch with no belai/ prefix has no card prefix: nothing to list.
	other, err := PrepareWorktree(context.Background(), ws.repo, kanban.Item{ID: escItem, Title: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Remove(context.Background())
	other.Branch = "feature"
	if prs, err := other.CardPRs(context.Background()); err != nil || prs != nil {
		t.Fatalf("a plain branch's PRs: %+v %v", prs, err)
	}
}

// TestCloseDuplicatePRClosesTheNamedOneAndKeepsTheOther covers the duplicate
// close through the real provider path: both pull requests come from this
// item's branches, the close carries the reason as its comment, and the kept
// one becomes the item's. worktree.go:931.
func TestCloseDuplicatePRClosesTheNamedOneAndKeepsTheOther(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	branch := ws.Branch
	// A second attempt branch of the same card, with its own pull request.
	other, err := PrepareWorktree(context.Background(), ws.repo, kanban.Item{ID: escItem, Title: "t", Attempts: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Remove(context.Background())
	os.WriteFile(filepath.Join(other.Dir, "more.txt"), []byte("more\n"), 0o644)
	if n, err := other.Commit(context.Background(), "belai: more"); err != nil || n != 1 {
		t.Fatalf("commit on the second branch: %d %v", n, err)
	}
	ghShim(t, `"pr list")
    echo '[{"number":21,"title":"a","state":"OPEN","url":"https://github.com/acme/app/pull/21","isDraft":true,"headRefName":"`+branch+`"},{"number":22,"title":"b","state":"OPEN","url":"https://github.com/acme/app/pull/22","isDraft":true,"headRefName":"`+other.Branch+`"}]' ;;
  "pr close")
    exit 0 ;;
  "pr comment")
    exit 0 ;;`)
	closed, kept, err := ws.CloseDuplicatePR(context.Background(), 21, 22, "superseded by the second attempt")
	if err != nil {
		t.Fatalf("close duplicate: %v", err)
	}
	if closed.Number != 21 || closed.Branch != branch || kept.Number != 22 || kept.Branch != other.Branch {
		t.Fatalf("closed %+v kept %+v", closed, kept)
	}
	if ws.Kept().Number != 22 {
		t.Fatalf("the item's pull request did not move to the kept one: %+v", ws.Kept())
	}
}

// TestCloseDuplicatePRRefusesOtherNumbers covers the guards: a number not
// among the item's open pull requests, and closing one's own as its own
// duplicate, are refused.
func TestCloseDuplicatePRRefusesOtherNumbers(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	ghShim(t, `"pr list")
    echo '[{"number":31,"title":"a","state":"OPEN","url":"https://github.com/acme/app/pull/31","isDraft":true,"headRefName":"`+ws.Branch+`"}]' ;;`)
	if _, _, err := ws.CloseDuplicatePR(context.Background(), 31, 31, "r"); err == nil || !strings.Contains(err.Error(), "cannot be a duplicate of itself") {
		t.Fatalf("self duplicate: %v", err)
	}
	if _, _, err := ws.CloseDuplicatePR(context.Background(), 31, 99, "r"); err == nil || !strings.Contains(err.Error(), "open ones from this item's branches") {
		t.Fatalf("unknown keep: %v", err)
	}
}

// TestOriginForgeCommentLeavesTheBodyOnTheCard covers originForge.Comment: the
// body goes to the forge CLI as one --body argument, through forgeAuthed, so
// the gh shim records it. conflicts.go:1240.
func TestOriginForgeCommentLeavesTheBodyOnTheCard(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	card := mergefix.Card{Repo: "acme/app", Number: 7, PR: "https://github.com/acme/app/pull/7"}
	var got []string
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1 $2" = "pr comment" ]; then
  for a in "$@"; do echo "$a"; done
  exit 0
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	real := ws.run
	ws.run = func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		out, err := real(ctx, dir, argv...)
		if err == nil && len(argv) > 0 && argv[0] == "gh" {
			got = append(got, strings.TrimSpace(string(out)))
		}
		return out, err
	}
	if err := (originForge{}).Comment(context.Background(), ws, card, "the attempt ledger"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0], "the attempt ledger") {
		t.Fatalf("the body did not reach gh: %q", got)
	}
}

// TestConflictAttemptPublishesAForksResolution covers the fork branch of
// conflictRun.attempt with the real Publish side: the fork's resolution is
// published as the worktree's own branch and commented, never pushed to the
// pull request. attempt and Publish sit at conflicts.go:376 and :1238.
func TestConflictAttemptPublishesAForksResolution(t *testing.T) {
	store, reg := testEnv(t)
	repo := conflictRepo(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "Resolve merge conflicts: owner/name#7", Body: strings.Replace(conflictCardBody, "fork: false", "fork: true", 1), Labels: []string{agentprofile.MergeConflictLabel}}, kanban.ProvenanceFor(repo, "s", "h"))
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeResolver{t: t, pick: follow}
	w, f := conflictWorker(t, store, reg, repo, r, func(string) bool { return false })
	var published []string
	w.Conflicts = &publishRecorder{fakeConflictForge: f, published: &published}
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Get(it.ID)
	if after.List != kanban.Review || len(published) != 1 {
		t.Fatalf("list %s published %v", after.List, published)
	}
	if !slices.Contains(notesOf(after), mergefix.FinalNotPushed(1, "the pull request is from a fork")) {
		t.Fatalf("notes %q", notesOf(after))
	}
}

// TestWorkerPublishRecordsTheDraftPullRequest covers Worker.publish both
// ways: a publish records the pull request on the item, and a failure lands
// as a note instead. worker.go:1718.
func TestWorkerPublishRecordsTheDraftPullRequest(t *testing.T) {
	store, reg := testEnv(t)
	ws, repo := githubWorktree(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "t", Labels: []string{"build"}}, kanban.ProvenanceFor(repo, "s", "h"))
	if err != nil {
		t.Fatal(err)
	}
	ghShim(t, `"pr view")
    exit 1 ;;
  "pr create")
    echo "https://github.com/acme/app/pull/41" ;;`)
	p := builderProfile()
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopComplete, Passes: 1}, nil
	})
	w.Repo = repo
	w.Settings.Worktrees = nil
	var pushArgs []string
	real := ws.run
	ws.run = func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if slices.Contains(argv, "push") {
			pushArgs = argv
			return []byte("branch pushed\n"), nil
		}
		return real(ctx, dir, argv...)
	}
	w.publish(context.Background(), ws, it, 1)
	got, _ := store.Get(it.ID)
	if got.PR != "https://github.com/acme/app/pull/41" {
		t.Fatalf("PR %q", got.PR)
	}
	_ = pushArgs
}

// TestWorkerPublishNotesAFailedPublish covers the other branch of
// Worker.publish: a refused publish lands as a note on the item, not an error
// to the caller.
func TestWorkerPublishNotesAFailedPublish(t *testing.T) {
	store, reg := testEnv(t)
	repo := gitRepo(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "t", Labels: []string{"build"}}, kanban.ProvenanceFor(repo, "s", "h"))
	if err != nil {
		t.Fatal(err)
	}
	ws, err := PrepareWorktree(context.Background(), repo, it, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Remove(context.Background())
	os.WriteFile(filepath.Join(ws.Dir, "fix.txt"), []byte("fixed\n"), 0o644)
	if n, err := ws.Commit(context.Background(), "belai: fix"); err != nil || n != 1 {
		t.Fatalf("commit %d %v", n, err)
	}
	p := builderProfile()
	w := newWorker(t, store, reg, p, func(ctx context.Context, tt Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopComplete, Passes: 1}, nil
	})
	w.Repo = repo
	w.Settings.Worktrees = nil
	w.publish(context.Background(), ws, it, 1)
	got, _ := store.Get(it.ID)
	if got.PR != "" || !strings.Contains(got.LastNote(), "draft pull request not opened") {
		t.Fatalf("PR %q note %q", got.PR, got.LastNote())
	}
}

// TestItemPublisherCloseDuplicatePRCoversTheWorkerWrapper covers the
// Worker-side wrapper at worker.go:2072: the same close through
// itemPublisher, which moves the item's pull request to the kept one.
func TestItemPublisherCloseDuplicatePRCoversTheWorkerWrapper(t *testing.T) {
	store, _ := testEnv(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "Fix the queue", Labels: []string{"build"}}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/acme/app.git").CombinedOutput(); err != nil {
		t.Fatalf("remote add: %v %s", err, out)
	}
	ws, err := PrepareWorktree(context.Background(), repo, it, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Remove(context.Background())
	os.WriteFile(filepath.Join(ws.Dir, "fix.txt"), []byte("fixed\n"), 0o644)
	if n, err := ws.Commit(context.Background(), "belai: fix"); err != nil || n != 1 {
		t.Fatalf("commit: %d %v", n, err)
	}
	branch := ws.Branch
	other, err := PrepareWorktree(context.Background(), repo, kanban.Item{ID: it.ID, Title: "t", Attempts: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Remove(context.Background())
	os.WriteFile(filepath.Join(other.Dir, "more.txt"), []byte("more\n"), 0o644)
	if n, err := other.Commit(context.Background(), "belai: more"); err != nil || n != 1 {
		t.Fatalf("commit: %d %v", n, err)
	}
	ghShim(t, `"pr list")
    echo '[{"number":51,"title":"a","state":"OPEN","url":"https://github.com/acme/app/pull/51","isDraft":true,"headRefName":"`+branch+`"},{"number":52,"title":"b","state":"OPEN","url":"https://github.com/acme/app/pull/52","isDraft":true,"headRefName":"`+other.Branch+`"}]' ;;
  "pr close")
    exit 0 ;;
  "pr comment")
    exit 0 ;;`)
	p := &itemPublisher{ws: ws, store: store, item: it.ID, session: "ses_test"}
	msg, err := p.CloseDuplicatePR(context.Background(), 51, 52, "superseded")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !strings.Contains(msg, "closed #51") || !strings.Contains(msg, "is now #52") {
		t.Fatalf("msg %q", msg)
	}
	if got, _ := store.Get(it.ID); got.PR != "https://github.com/acme/app/pull/52" {
		t.Fatalf("item PR %q", got.PR)
	}
}

// TestForgeTitleOneLineAndCapped covers ForgeTitle's own branches: control
// markup and newlines are cleaned to one line, and the line is capped at 120
// runes.
func TestForgeTitleOneLineAndCapped(t *testing.T) {
	if got := ForgeTitle("Fix the \x1b[31mqueue\x1b[0m\nnow"); got != "Fix the queue now" {
		t.Fatalf("ForgeTitle %q", got)
	}
	long := strings.Repeat("word ", 40)
	if got := ForgeTitle(long); len([]rune(got)) > 120 {
		t.Fatalf("ForgeTitle capped at %d, want at most 120", len([]rune(got)))
	}
}

// TestDiffStatCoversTheBranchWork covers DiffStat on a branch with a commit
// and outside a worktree: the stat names the file and nothing else.
// worktree.go:457.
func TestDiffStatCoversTheBranchWork(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	out := ws.DiffStat(context.Background())
	if !strings.Contains(out, "fix.txt") || !strings.Contains(out, "|") {
		t.Fatalf("diff stat %q", out)
	}
	// A plain directory is not a worktree: nothing is reported.
	if (&Workspace{}).DiffStat(context.Background()) != "" {
		t.Fatal("a plain directory produced a diff stat")
	}
}

// serveGitDaemon starts git daemon on a loopback port serving dir's parent, so
// the origin git://127.0.0.1:<port>/<basename> names dir, and stops it when
// the test ends.
func serveGitDaemon(t *testing.T, dir string) (url string, stop func()) {
	t.Helper()
	port, err := localinfer.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	parent, base := filepath.Dir(dir), filepath.Base(dir)
	url = "git://127.0.0.1:" + strconv.Itoa(port) + "/" + base
	// Run git-daemon itself, not the "git daemon" wrapper: killing the wrapper
	// on cleanup left its git-daemon child listening after the test ended.
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}
	daemon := filepath.Join(strings.TrimSpace(string(execPath)), "git-daemon")
	cmd := exec.Command(daemon, "--listen=127.0.0.1", "--port="+strconv.Itoa(port),
		"--base-path="+parent, "--export-all", "--forbid-override=receive-pack", "--forbid-override=upload-archive", parent)
	if err := cmd.Start(); err != nil {
		t.Skipf("git daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	// Wait for the daemon's port, or skip: a sandbox that refuses listeners
	// cannot serve a local fetch.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Skip("git daemon did not listen")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return url, func() {}
}

// TestOriginForgeFetchResolvesTheCardRefs covers originForge.Fetch against a
// local git daemon standing in for origin: the head and base the card names
// are fetched into the refs/belai/pull refs and resolved in the repository.
// conflicts.go:1178.
func TestOriginForgeFetchResolvesTheCardRefs(t *testing.T) {
	testEnv(t)
	repo := conflictRepo(t)
	// Point the fixture's origin at the daemon so originForge.Fetch stays
	// offline. The daemon serves the repo's parent; the origin names its basename.
	url, stop := serveGitDaemon(t, repo)
	defer stop()
	gitIn(t, repo, "remote", "set-url", "origin", url)
	card := mergefix.Card{Repo: "owner/name", Number: 7, PR: "https://github.com/owner/name/pull/7", Head: "feature", Base: "main"}
	refs, err := (originForge{}).Fetch(context.Background(), repo, card)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if refs.Head != gitIn(t, repo, "rev-parse", "feature") || refs.Base != gitIn(t, repo, "rev-parse", "main") {
		t.Fatalf("refs %+v", refs)
	}
	if _, err := git(context.Background(), hardenedGit("", ""), repo, "rev-parse", "--verify", "--end-of-options", "refs/belai/pull/7/head^{commit}"); err != nil {
		t.Fatalf("the fetched head ref is missing: %v", err)
	}
}

// TestOriginForgePushMovesThePullRequestHead covers originForge.Push: the
// merge commit is pushed to the pull request's head branch through the
// worktree's runner, never forced. conflicts.go:1217.
func TestOriginForgePushMovesThePullRequestHead(t *testing.T) {
	testEnv(t)
	repo := conflictRepo(t)
	ws, err := PrepareWorktree(context.Background(), repo, kanban.Item{ID: "7f9a2c00-0000-4000-8000-000000000004", Title: "t"}, "refs/heads/feature")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Remove(context.Background())
	head, base := gitIn(t, repo, "rev-parse", "feature"), gitIn(t, repo, "rev-parse", "main")
	tree, err := ws.stageTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sha, err := ws.commitMerge(context.Background(), tree, head, base, "Merge main into feature")
	if err != nil {
		t.Fatalf("commit merge: %v", err)
	}
	var pushArgs []string
	real := ws.run
	ws.run = func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		for _, a := range argv {
			if a == "--force" || a == "-f" || a == "+" {
				t.Errorf("a forced push was attempted: %v", argv)
			}
		}
		if i := slices.Index(argv, "push"); i > 0 {
			pushArgs = argv
			return []byte("to https://github.com/owner/name.git\n"), nil
		}
		return real(ctx, dir, argv...)
	}
	if err := (originForge{}).Push(context.Background(), ws, mergefix.Card{Head: "feature"}, sha); err != nil {
		t.Fatalf("push: %v", err)
	}
	if i := slices.Index(pushArgs, "push"); i < 0 || !slices.Contains(pushArgs, sha+":refs/heads/feature") {
		t.Fatalf("push args %v", pushArgs)
	}
}

// TestOriginForgePublishDelegatesToPushBranch covers originForge.Publish: the
// conflict loop's publish is the worktree's PushBranch. conflicts.go:1238.
func TestOriginForgePublishDelegatesToPushBranch(t *testing.T) {
	testEnv(t)
	ws, _ := githubWorktree(t)
	real := ws.run
	pushes := 0
	ws.run = func(ctx context.Context, dir string, argv ...string) ([]byte, error) {
		if slices.Contains(argv, "push") {
			pushes++
			return []byte("branch pushed\n"), nil
		}
		return real(ctx, dir, argv...)
	}
	if err := (originForge{}).Publish(context.Background(), ws); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pushes != 1 {
		t.Fatalf("pushes = %d", pushes)
	}
}
