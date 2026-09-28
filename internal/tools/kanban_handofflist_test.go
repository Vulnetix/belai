package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// A claim with HandoffList sends every handoff there, whatever list the
// model asks for; without it the model's choice (default backlog) stands.
func TestHandoffListIsForced(t *testing.T) {
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	parent, _, _ := store.Add(kanban.ItemInput{Title: "survey"}, kanban.Provenance{})
	for _, c := range []struct {
		forced kanban.List
		asked  string
		want   kanban.List
	}{
		{kanban.Review, "backlog", kanban.Review},
		{kanban.Review, "", kanban.Review},
		{"", "", kanban.Backlog},
		{"", "review", kanban.Review},
		{kanban.Backlog, "review", kanban.Backlog},
	} {
		claim := &WorkerClaim{Worker: "w1", Item: parent.ID, HandoffLabels: []string{"build"}, HandoffList: c.forced}
		h := KanbanHandoff{KanbanBase{Store: store, Claim: claim}}
		args := map[string]any{"title": "task " + string(c.forced) + "/" + c.asked, "labels": []any{"build"}}
		if c.asked != "" {
			args["list"] = c.asked
		}
		if _, err := h.Execute(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		it, _ := store.Get(claim.HandedOff()[0])
		if it.List != c.want {
			t.Errorf("forced %q asked %q: landed in %s, want %s", c.forced, c.asked, it.List, c.want)
		}
	}
}
