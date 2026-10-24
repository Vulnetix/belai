package kanban

import (
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// The Software Factory engine (vdb-site) writes a crew step's card before it knows the host: project is
// owner/repo, and since vdb-site e6a506d it binds the card's cwd and projectKey (the WorkdirKey of the
// checkout) once a host takes the dispatch. This pins that a card of that shape, merged the way a
// host pulls it, is claimable by the requests the scout, repro and lint-fixer workers build.
func TestEngineCardIsClaimable(t *testing.T) {
	wire := func(id, project, key, cwd, assignee string, labels ...string) sessionsync.KanbanItem {
		return sessionsync.KanbanItem{
			ID: id, Title: "Sweep the tests (run_b62ff9cb)", Body: "body", List: "backlog",
			Project: project, ProjectKey: key, Cwd: cwd,
			CreatedAt: 1792728233748, UpdatedAt: 1792800526553, Version: 41446,
			Agent: &sessionsync.KanbanAgent{Labels: labels, Assignee: assignee},
		}
	}
	cases := []struct {
		name string
		it   sessionsync.KanbanItem
		req  ClaimRequest
	}{
		{"scout in crew", wire("60e10aff-b614-5cad-9838-c88fc1b1f01d", "vulnetix/vdb-site", "vdb-site-69f15847", "/workspace/repos/Vulnetix/vdb-site", "crew:belai:delivery", "factory", "scout", "wf-sys-test-sweep"),
			ClaimRequest{Profile: "belai:scout", Crew: "belai:delivery", Project: "vdb-site-69f15847", Lists: []List{Backlog}, Labels: []string{"scout"}, Worker: "w1", Host: "h", Lease: time.Minute}},
		{"repro in crew", wire("8ffa7b00-0000-5000-8000-000000000000", "vulnetix/mcp-server", "mcp-server-c4a87d99", "/workspace/repos/Vulnetix/mcp-server", "crew:belai:bug", "factory", "repro", "wf-sys-ci-fix"),
			ClaimRequest{Profile: "belai:repro", Crew: "belai:bug", Project: "mcp-server-c4a87d99", Lists: []List{Backlog}, Labels: []string{"repro"}, Worker: "w1", Host: "h", Lease: time.Minute}},
		{"lint-fixer", wire("7791bc00-0000-5000-8000-000000000000", "vulnetix/vulnetix-vscode", "vulnetix-vscode-de627585", "/workspace/repos/Vulnetix/vulnetix-vscode", "belai:lint-fixer", "factory", "lint-drift", "wf-sys-lint-sweep"),
			ClaimRequest{Profile: "belai:lint-fixer", Project: "vulnetix-vscode-de627585", Lists: []List{Backlog}, Labels: []string{"lint-drift"}, Worker: "w1", Host: "h", Lease: time.Minute}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := testStore(t)
			if _, err := s.Merge([]Item{FromWire(c.it)}, 41446); err != nil {
				t.Fatal(err)
			}
			got, err := s.Claim(c.req)
			if err != nil {
				t.Fatalf("claim: %v (item merged but not claimable)", err)
			}
			if got.ID != c.it.ID {
				t.Fatalf("claimed %s", got.ID)
			}
		})
	}
}

// Before vdb-site bound the checkout, the engine's card carried no projectKey. A worker started in its own
// project claims by that project's key, so it never took the card: they sat in backlog for hours.
func TestEngineCardWithoutProjectKeyIsNotClaimableByAProjectScopedWorker(t *testing.T) {
	s := testStore(t)
	w := sessionsync.KanbanItem{
		ID: "60e10aff-b614-5cad-9838-c88fc1b1f01d", Title: "Sweep the tests", List: "backlog", Project: "vulnetix/vdb-site",
		CreatedAt: 1, UpdatedAt: 2, Version: 3, Agent: &sessionsync.KanbanAgent{Labels: []string{"factory", "scout"}, Assignee: "crew:belai:delivery"},
	}
	if _, err := s.Merge([]Item{FromWire(w)}, 3); err != nil {
		t.Fatal(err)
	}
	req := ClaimRequest{Profile: "belai:scout", Crew: "belai:delivery", Project: "vdb-site-69f15847", Lists: []List{Backlog}, Labels: []string{"scout"}, Worker: "w1", Host: "h", Lease: time.Minute}
	if _, err := s.Claim(req); err == nil {
		t.Fatal("a card with no projectKey was claimed by a project-scoped worker")
	}
	// A worker with project "all" still takes it.
	req.Project = ""
	if _, err := s.Claim(req); err != nil {
		t.Fatalf("an unscoped worker could not claim it: %v", err)
	}
}
