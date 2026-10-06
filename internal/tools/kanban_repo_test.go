package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/repoindex"
)

// gitRepo makes a git checkout at dir whose origin is remote.
func gitRepo(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
}

// folder is a plain directory (the worker's) holding two checkouts, with a
// third, whose name clashes with one of them, in a sibling directory. The repo
// index scans the parent of the working directory, so it sees all three.
type folder struct {
	root                string
	website, api, other string
	ix                  repoindex.Index
}

func newFolder(t *testing.T) folder {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	f := folder{root: work, website: filepath.Join(work, "website"), api: filepath.Join(work, "api"), other: filepath.Join(base, "forks", "website")}
	gitRepo(t, f.website, "https://github.com/acme/website.git")
	gitRepo(t, f.api, "https://github.com/acme/api.git")
	gitRepo(t, f.other, "https://github.com/other/website.git")
	f.ix = repoindex.Scan(context.Background(), work)
	return f
}

// repoHandoff is a handoff tool for a worker in the folder, holding an item in
// the folder's own project, with the repo argument on or off.
func repoHandoff(t *testing.T, f folder, repos bool) (KanbanHandoff, *WorkerClaim, *kanban.Store, kanban.Provenance) {
	t.Helper()
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	own := kanban.ProvenanceFor(f.root, "sess-live", "host-1")
	src := kanban.NewSource(own)
	parent, _, err := store.Add(kanban.ItemInput{Title: "Analyze CloudWatch logs", Labels: []string{"log-analysis", "survey"}}, own)
	if err != nil {
		t.Fatal(err)
	}
	claim := &WorkerClaim{Worker: "w1", Item: parent.ID, Profile: "aws-log-analyzer", HandoffLabels: []string{"infra"}, HandoffRepos: repos}
	if repos {
		claim.UseRepoIndex(f.ix)
	}
	return KanbanHandoff{KanbanBase{Store: store, Source: src, Claim: claim}}, claim, store, own
}

func handoffArgs(title, repo string) map[string]any {
	a := map[string]any{"title": title, "body": "evidence", "labels": []any{"infra"}}
	if repo != "" {
		a["repo"] = repo
	}
	return a
}

func allItems(t *testing.T, store *kanban.Store) []kanban.Item {
	t.Helper()
	items, err := store.Search(kanban.Query{})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// The repo argument exists only for a profile that opts in, and a call that
// names one without the opt-in is refused.
func TestHandoffRepoIsOffUnlessTheProfileOptsIn(t *testing.T) {
	f := newFolder(t)
	off, _, store, _ := repoHandoff(t, f, false)
	if _, ok := off.Definition().Properties["repo"]; ok {
		t.Fatal("a profile without handoff_repos must not see a repo argument")
	}
	if _, err := off.Execute(context.Background(), handoffArgs("Fix it", "acme/website")); err == nil {
		t.Fatal("a repo argument without the opt-in must be refused")
	}
	if n := len(allItems(t, store)); n != 1 {
		t.Errorf("%d items on the board, want only the claimed one", n)
	}
	on, _, _, _ := repoHandoff(t, f, true)
	if _, ok := on.Definition().Properties["repo"]; !ok {
		t.Fatal("handoff_repos should add the repo argument")
	}
	for name := range on.Definition().Properties {
		if name == "dir" || name == "project" || name == "path" || name == "session" || name == "project_key" {
			t.Errorf("the handoff tool takes a %q argument", name)
		}
	}
}

// A handoff that names a repo is filed under that checkout's project and
// directory, derived by the harness; the worker's session and host stay on it,
// and it still links to the item the worker holds.
func TestHandoffRepoFilesUnderThatRepositorysProject(t *testing.T) {
	f := newFolder(t)
	h, claim, store, own := repoHandoff(t, f, true)
	res, err := h.Execute(context.Background(), handoffArgs("Raise the queue's visibility timeout", "acme/website"))
	if err != nil {
		t.Fatal(err)
	}
	want := kanban.ProvenanceFor(f.website, "", "")
	var got kanban.Item
	for _, it := range allItems(t, store) {
		if it.Title == "Raise the queue's visibility timeout" {
			got = it
		}
	}
	if got.ID == "" {
		t.Fatal("the handoff was not filed")
	}
	if got.ProjectKey != want.ProjectKey || got.Project != want.Project || got.Dir != want.Dir {
		t.Errorf("filed under %q %q %q, want the website checkout %q %q %q", got.Project, got.ProjectKey, got.Dir, want.Project, want.ProjectKey, want.Dir)
	}
	if got.ProjectKey == own.ProjectKey {
		t.Error("the item stayed in the worker's own project")
	}
	if got.SessionID != "sess-live" || got.HostID != "host-1" {
		t.Errorf("session %q host %q: the worker's stay on the item", got.SessionID, got.HostID)
	}
	if got.Parent != claim.Item || got.Hops != 1 {
		t.Errorf("parent %q hops %d", got.Parent, got.Hops)
	}
	if !strings.Contains(res.Content, "under "+want.Project) {
		t.Errorf("the confirmation should name the project: %q", res.Content)
	}
	if !claim.owns(got.ID) {
		t.Error("the worker should own an item it handed off, in any project")
	}
}

func TestHandoffWithoutARepoStaysInTheWorkersProject(t *testing.T) {
	f := newFolder(t)
	h, _, store, own := repoHandoff(t, f, true)
	res, err := h.Execute(context.Background(), handoffArgs("Tidy the alarm names", ""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, " under ") {
		t.Errorf("no repo, so nothing to name: %q", res.Content)
	}
	for _, it := range allItems(t, store) {
		if it.Title == "Tidy the alarm names" && it.ProjectKey != own.ProjectKey {
			t.Errorf("filed under %q, want the worker's project", it.Project)
		}
	}
}

// Text never becomes provenance: a path, a project name, a repo that is not in
// the index and an ambiguous bare name are all refused, and nothing is filed.
func TestHandoffRepoRefusesAnythingNotInTheIndex(t *testing.T) {
	f := newFolder(t)
	h, _, store, _ := repoHandoff(t, f, true)
	for _, bad := range []string{
		"/etc", f.website, "../website", "acme/missing", "missing", "acme/", "/",
		"website", // two checkouts share the bare name
		"ACME/WEBSITE/extra", "https://github.com/acme/website", "acme/website\nVulnetix",
	} {
		if _, err := h.Execute(context.Background(), handoffArgs("Fix it", bad)); err == nil {
			t.Errorf("repo %q accepted", bad)
		}
	}
	if n := len(allItems(t, store)); n != 1 {
		t.Errorf("%d items on the board after refusals, want only the claimed one", n)
	}
	_, err := h.Execute(context.Background(), handoffArgs("Fix it", "acme/missing"))
	if err == nil {
		t.Fatal("unknown repo accepted")
	}
	for _, name := range []string{"acme/website", "acme/api", "other/website"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal should list %q: %v", name, err)
		}
	}
}

// The index is matched case-insensitively, and a bare name works only when
// exactly one checkout has it.
func TestHandoffRepoNameMatching(t *testing.T) {
	f := newFolder(t)
	h, _, store, _ := repoHandoff(t, f, true)
	for _, ref := range []string{"ACME/Website", "acme/api", "api", "Other/WEBSITE"} {
		if _, err := h.Execute(context.Background(), handoffArgs("Fix "+ref, ref)); err != nil {
			t.Errorf("repo %q: %v", ref, err)
		}
	}
	if _, err := h.Execute(context.Background(), handoffArgs("Fix bare", "website")); err == nil {
		t.Error("a bare name two checkouts share must be refused")
	}
	if n := len(allItems(t, store)); n != 5 {
		t.Errorf("%d items, want the claimed one and four handoffs", n)
	}
}

// The board refuses a second open item with a title in the same project, and
// the repo's project is the one that counts.
func TestHandoffRepoDuplicatesArePerProject(t *testing.T) {
	f := newFolder(t)
	h, _, store, _ := repoHandoff(t, f, true)
	for _, c := range []struct {
		repo string
		dup  bool
	}{{"acme/website", false}, {"acme/api", false}, {"acme/website", true}, {"", false}, {"", true}} {
		res, err := h.Execute(context.Background(), handoffArgs("Raise the timeout", c.repo))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(res.Content, "already on the board"); got != c.dup {
			t.Errorf("repo %q: duplicate = %v, want %v (%s)", c.repo, got, c.dup, res.Content)
		}
	}
	if n := len(allItems(t, store)); n != 4 {
		t.Errorf("%d items, want the claimed one and three distinct handoffs", n)
	}
}

// A survey's rule that its handoffs wait in review holds for a handoff filed
// under another repository too: the repo moves the project, not the list.
func TestHandoffRepoKeepsTheSurveyList(t *testing.T) {
	f := newFolder(t)
	h, claim, store, _ := repoHandoff(t, f, true)
	claim.HandoffList = kanban.Review
	args := handoffArgs("Raise the timeout", "acme/website")
	args["list"] = "backlog"
	if _, err := h.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	for _, it := range allItems(t, store) {
		if it.Title == "Raise the timeout" && it.List != kanban.Review {
			t.Errorf("filed in %s, want review", it.List)
		}
	}
}

// The labels a handoff may carry are still the profile's: naming a repo does
// not widen them.
func TestHandoffRepoStillChecksLabels(t *testing.T) {
	f := newFolder(t)
	h, _, _, _ := repoHandoff(t, f, true)
	args := handoffArgs("Fix it", "acme/website")
	args["labels"] = []any{"build"}
	if _, err := h.Execute(context.Background(), args); err == nil {
		t.Fatal("a label the profile does not allow was accepted")
	}
}

// With no repositories beneath the worker's directory every name is refused
// and the refusal lists nothing.
func TestHandoffRepoWithAnEmptyIndex(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	empty := folder{root: work}
	empty.ix = repoindex.Scan(context.Background(), work)
	h, _, _, _ := repoHandoff(t, empty, true)
	_, err := h.Execute(context.Background(), handoffArgs("Fix it", "acme/website"))
	if err == nil || strings.Contains(err.Error(), "acme/website:") {
		t.Fatalf("err = %v", err)
	}
}

// A checkout with no origin remote has no owner or name, so the index cannot
// name it; the handoff names it by its directory, and the refusal lists it the
// same way.
func TestHandoffRepoNamesACheckoutWithoutARemoteByItsDirectory(t *testing.T) {
	f := newFolder(t)
	local := filepath.Join(f.root, "scratch-repo")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", local, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	f.ix = repoindex.Scan(context.Background(), f.root)
	h, _, store, _ := repoHandoff(t, f, true)

	if _, err := h.Execute(context.Background(), handoffArgs("Fix scratch", "Scratch-Repo")); err != nil {
		t.Fatalf("a checkout with no remote, by its directory name: %v", err)
	}
	want := kanban.ProvenanceFor(local, "", "")
	found := false
	for _, it := range allItems(t, store) {
		if it.Title == "Fix scratch" {
			found = it.Dir == want.Dir && it.ProjectKey == want.ProjectKey
		}
	}
	if !found {
		t.Error("the handoff was not filed under the remote-less checkout")
	}
	_, err := h.Execute(context.Background(), handoffArgs("Fix it", "nope"))
	if err == nil || !strings.Contains(err.Error(), "scratch-repo") {
		t.Errorf("the refusal should list the checkout by its directory: %v", err)
	}
}
