package kanban

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

func TestNormRemote(t *testing.T) {
	for in, want := range map[string]string{
		"Vulnetix/VDB-Site":       "vulnetix/vdb-site",
		"vulnetix/vdb-site.git":   "vulnetix/vdb-site",
		" acme/api ":              "acme/api",
		"group/sub/project":       "group/sub/project",
		"vdb-site":                "",
		"":                        "",
		"https://github.com/a/b":  "",
		"acme/..":                 "",
		"acme/./x":                "",
		"-x/api":                  "",
		"acme/api\x00":            "",
		"acme/api with space":     "",
		"vdb-site-69f15847":       "",
		"acme/api/../../etc/pass": "",
	} {
		if got := NormRemote(in); got != want {
			t.Errorf("NormRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A card filed on one machine carries that machine's project key. A worker on
// another machine's checkout of the same repository claims by the remote, both
// for a card a host filed (Remote) and for a Software Factory card (Project is
// owner/name).
func TestClaimMatchesTheRepositoryAcrossMachines(t *testing.T) {
	card := func(id, project, key, remote string) sessionsync.KanbanItem {
		return sessionsync.KanbanItem{
			ID: id, Title: "Fix the flaky test", List: "backlog", Project: project, ProjectKey: key, Remote: remote,
			CreatedAt: 1, UpdatedAt: 2, Version: 3, Agent: &sessionsync.KanbanAgent{Labels: []string{"tests-failing"}},
		}
	}
	req := func(remote string) ClaimRequest {
		return ClaimRequest{Profile: "belai:test-fixer", Project: "vdb-site-0badc0de", Remote: remote, Lists: []List{Backlog},
			Labels: []string{"tests-failing"}, Worker: "w1", Host: "laptop", Lease: time.Minute}
	}
	cases := []struct {
		name  string
		card  sessionsync.KanbanItem
		req   ClaimRequest
		claim bool
	}{
		{"filed on another machine, matched by remote", card("11111111-1111-4111-8111-111111111111", "vdb-site", "vdb-site-69f15847", "Vulnetix/vdb-site"), req("vulnetix/vdb-site"), true},
		{"a factory card, matched by its owner/name project", card("22222222-2222-4222-8222-222222222222", "Vulnetix/vdb-site", "", ""), req("vulnetix/vdb-site"), true},
		{"another repository is never matched", card("33333333-3333-4333-8333-333333333333", "vdb-site", "vdb-site-69f15847", "someone/vdb-site"), req("vulnetix/vdb-site"), false},
		{"a worker without a remote keeps the key rule", card("44444444-4444-4444-8444-444444444444", "vdb-site", "vdb-site-69f15847", "vulnetix/vdb-site"), req(""), false},
		{"the same repository name alone is not enough", card("55555555-5555-4555-8555-555555555555", "vdb-site", "vdb-site-69f15847", ""), req("vulnetix/vdb-site"), false},
		{"the key still matches without a remote", card("66666666-6666-4666-8666-666666666666", "vdb-site", "vdb-site-0badc0de", ""), req(""), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := testStore(t)
			if _, err := s.Merge([]Item{FromWire(c.card)}, 3); err != nil {
				t.Fatal(err)
			}
			got, err := s.Claim(c.req)
			if c.claim && (err != nil || got.ID != c.card.ID) {
				t.Fatalf("not claimed: %v", err)
			}
			if !c.claim && err == nil {
				t.Fatalf("claimed %s", got.ID)
			}
		})
	}
}

// The remote travels on the wire, and a pull from a backend that predates it
// keeps the host's own value.
func TestRemoteSurvivesSyncAndAnOlderBackend(t *testing.T) {
	s := testStore(t)
	p := prov
	p.Remote = "Acme/API.git"
	it, _, err := s.Add(ItemInput{Title: "a card", List: Backlog}, p)
	if err != nil {
		t.Fatal(err)
	}
	if it.Remote != "acme/api" {
		t.Fatalf("remote %q", it.Remote)
	}
	w := ToWire(it)
	if w.Remote != "acme/api" || FromWire(w).Remote != "acme/api" {
		t.Fatalf("wire %q", w.Remote)
	}
	old := w
	old.Remote, old.Title, old.UpdatedAt, old.Version = "", "renamed on the web", it.Updated+10, 99
	if _, err := s.Merge([]Item{FromWire(old)}, 99); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(it.Short())
	if err != nil || got.Title != "renamed on the web" || got.Remote != "acme/api" {
		t.Fatalf("after an older backend's pull: %+v %v", got, err)
	}
	bad := w
	bad.Remote, bad.UpdatedAt, bad.Version = "https://evil.example/x", it.Updated+20, 100
	if _, err := s.Merge([]Item{FromWire(bad)}, 100); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(it.Short()); got.Remote != "acme/api" {
		t.Fatalf("a malformed remote replaced the host's: %q", got.Remote)
	}
}

func TestRemoteForReadsTheOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:Vulnetix/Belai.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if got := RemoteFor(dir); got != "vulnetix/belai" {
		t.Fatalf("RemoteFor = %q", got)
	}
	if got := ProvenanceFor(filepath.Join(dir), "s", "h").Remote; got != "vulnetix/belai" {
		t.Fatalf("ProvenanceFor remote = %q", got)
	}
	if got := RemoteFor(t.TempDir()); got != "" {
		t.Fatalf("a directory with no checkout has remote %q", got)
	}
}
