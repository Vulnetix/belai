package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

func kanbanRun(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := runKanbanCLI(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("belai kanban %v: exit %d: %s%s", args, code, out.String(), errOut.String())
	}
	return strings.TrimSpace(out.String())
}

func TestKanbanAssignRoutesToProfileAndCrew(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	id := strings.Fields(kanbanRun(t, "add", "Check the branch", "-label", "extra"))[0]

	// belai:reviewer claims needs-review from Review: the item moves there.
	kanbanRun(t, "assign", id, "belai:reviewer", "-host", "11111111-1111-4111-8111-111111111111")
	s, err := kanban.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if it.Assignee != "belai:reviewer" || it.List != kanban.Review || it.PinHost != "11111111-1111-4111-8111-111111111111" ||
		strings.Join(it.Labels, ",") != "extra,needs-review" {
		t.Fatalf("assigned item %+v", it)
	}

	// A crew routes by its entry member's labels and clears nothing else.
	id2 := strings.Fields(kanbanRun(t, "add", "Survey tests"))[0]
	kanbanRun(t, "assign", id2, "-crew", "belai:delivery", "-host", "none")
	it2, _ := s.Get(id2)
	if it2.Assignee != "" || it2.List != kanban.Backlog || strings.Join(it2.Labels, ",") != "scout" || it2.PinHost != "" {
		t.Fatalf("crew-assigned item %+v", it2)
	}

	var out, errOut bytes.Buffer
	if code := runKanbanCLI([]string{"assign", id2, "belai:nobody"}, strings.NewReader(""), &out, &errOut); code == 0 {
		t.Fatal("unknown profile accepted")
	}
	if code := runKanbanCLI([]string{"assign", id2}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("missing profile exit %d", code)
	}
}
