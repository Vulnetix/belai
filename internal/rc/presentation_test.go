package rc

import (
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The catalogue carries how an agent is presented, so the console can draw it,
// and re-advertises when that changes.
func TestCatalogueCarriesPresentationAndReadvertisesOnChange(t *testing.T) {
	p := agentprofile.AgentProfile{
		Name: "reviewer", Mode: agentprofile.ModeWorker, Kanban: &agentprofile.KanbanSpec{Labels: []string{"build"}},
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", DisplayName: "Dep Reviewer", AvatarID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Palette: []string{"#006860", "#00b8a5", "#33867f", "#66d1c4"},
	}
	got := profileSummary(p)

	if got.ID != p.ID || got.DisplayName != "Dep Reviewer" || got.AvatarID != p.AvatarID || !slices.Equal(got.Palette, p.Palette) {
		t.Fatalf("summary = %+v", got)
	}
	// A copy: the caller's palette is not shared with the advertisement.
	p.Palette[0] = "#000000"
	if got.Palette[0] != "#006860" {
		t.Fatal("the advertised palette aliases the profile's")
	}
	// A profile with none says none.
	plain := profileSummary(agentprofile.AgentProfile{Name: "plain", Mode: agentprofile.ModeWorker, Kanban: &agentprofile.KanbanSpec{}})
	if plain.ID != "" || plain.DisplayName != "" || len(plain.Palette) != 0 || plain.AvatarID != "" {
		t.Fatalf("a profile without presentation: %+v", plain)
	}
	a := Inventory{Profiles: []sessionsync.RCProfile{got}}
	b := Inventory{Profiles: []sessionsync.RCProfile{{Name: got.Name, Lists: got.Lists, DisplayName: "Renamed", ID: got.ID}}}
	if a.catalogueHash() == b.catalogueHash() {
		t.Fatal("a new display name must change the catalogue hash, or the website never hears of it")
	}
}
