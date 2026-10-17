package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/kanban"
)

// Each flow's entry label is the one its crew's first stage claims, and that
// stage holds what it hands off, so the flag cannot drift from the profiles.
func TestFlowEntryLabelsAreTheFirstStagesOwn(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	first := map[string]string{"feature": "belai:planner", "bug": "belai:repro", "ui": "belai:designer"}
	if len(first) != len(flowEntryLabels) {
		t.Fatalf("flows %v, profiles %v", flowNames(), first)
	}
	for flow, name := range first {
		p, err := agentprofile.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(p.Kanban.Labels, []string{flowEntryLabels[flow]}) {
			t.Errorf("%s claims %q, but -flow %s files %q", name, p.Kanban.Labels, flow, flowEntryLabels[flow])
		}
		if !kanban.ValidCheckpoint(p.Kanban.HandoffCheckpoint) {
			t.Errorf("%s holds its handoffs at %q", name, p.Kanban.HandoffCheckpoint)
		}
		crew, err := agentprofile.LoadCrew("belai:" + flow)
		if err != nil || crew.Members[0].Profile != name {
			t.Errorf("crew belai:%s starts with %v (%v), want %s", flow, crew.Members, err, name)
		}
	}
}

func TestKanbanFlowAndCheckpointDecisions(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := runKanbanCLI(args, strings.NewReader(""), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	if code, _, e := run("add", "-flow", "nonsense", "title"); code == 0 || !strings.Contains(e, "bug, feature, ui") {
		t.Fatalf("an unknown flow: %d %q", code, e)
	}
	code, out, e := run("add", "-flow", "feature", "add", "the", "export")
	if code != 0 {
		t.Fatalf("add: %d %s", code, e)
	}
	id := strings.TrimSpace(out)
	if code, shown, _ := run("show", id); code != 0 || !strings.Contains(shown, "plan") {
		t.Fatalf("the card does not carry the flow's entry label: %s", shown)
	}

	// Nothing holds this card, so a decision on it is refused.
	if code, _, e := run("checkpoint", id, "approve"); code == 0 || !strings.Contains(e, "not held at a checkpoint") {
		t.Fatalf("approving an unheld card: %d %q", code, e)
	}

	code, out, e = run("add", "-label", kanban.CheckpointLabel(kanban.CheckpointPlanApproval), "-label", "build", "a held task")
	if code != 0 {
		t.Fatalf("add held: %d %s", code, e)
	}
	held := strings.TrimSpace(out)
	if code, _, e := run("checkpoint", held, "maybe"); code != 2 {
		t.Fatalf("a bad verb: %d %q", code, e)
	}
	if code, out, e := run("checkpoint", held, "reject", "-to", "nowhere"); code != 2 {
		t.Fatalf("a bad list: %d %q %q", code, out, e)
	}
	if code, out, e := run("checkpoint", held, "reject", "-note", "tests missing"); code != 0 || !strings.Contains(out, "backlog") {
		t.Fatalf("reject: %d %q %q", code, out, e)
	}
	if code, shown, _ := run("show", held); code != 0 || strings.Contains(shown, "checkpoint:") || !strings.Contains(shown, "rejected: tests missing") {
		t.Fatalf("after reject: %s", shown)
	}
}
