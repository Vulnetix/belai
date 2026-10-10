package agentprofile

import "testing"

// A worker decides when its deliverable is a recorded decision, not a change:
// the goal loop then never tells it to make an edit.
func TestBuiltinWorkersThatDecide(t *testing.T) {
	want := map[string]bool{
		"belai:scout":      true,  // read-only worktree
		"belai:reviewer":   true,  // decides manual gates, though it may edit the crew notes
		"belai:vuln-scout": true,  // no file tool
		"belai:verifier":   true,  // no file tool; Bash only runs checks
		"belai:builder":    false, // changes the code
		"belai:patcher":    false,
	}
	for _, name := range workerProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Decides(); got != want[name] {
			t.Errorf("%s: Decides() = %v, want %v", name, got, want[name])
		}
	}
}

func TestDecidesFollowsTheDeclarations(t *testing.T) {
	cases := []struct {
		name string
		p    AgentProfile
		want bool
	}{
		{"full surface edits", AgentProfile{}, false},
		{"file tool edits", AgentProfile{Tools: []string{"Read", "Edit"}}, false},
		{"bash alone only checks", AgentProfile{Tools: []string{"Read", "Bash"}}, true},
		{"read only workspace", AgentProfile{Workspace: &WorkspaceSpec{ReadOnly: true}}, true},
		{"gate review", AgentProfile{Tools: []string{"Edit"}, Kanban: &KanbanSpec{Gates: &GatesSpec{Review: true}}}, true},
		{"gates without review", AgentProfile{Tools: []string{"Edit"}, Kanban: &KanbanSpec{Gates: &GatesSpec{Require: true}}}, false},
	}
	for _, c := range cases {
		if got := c.p.Decides(); got != c.want {
			t.Errorf("%s: Decides() = %v, want %v", c.name, got, c.want)
		}
	}
}
