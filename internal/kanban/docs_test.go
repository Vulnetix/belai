package kanban

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// docs/fleet.md and docs/kanban.md state who an assignee names. The prefixes
// are code: a change to ParseAssignee must change both pages.
func TestAssigneeKindsAreDocumented(t *testing.T) {
	for _, page := range []string{"docs/fleet.md", "docs/kanban.md"} {
		doc := docparity.Read(t, page)
		for _, prefix := range []string{"worker:", "crew:", "person:"} {
			if !strings.Contains(doc, prefix) {
				t.Errorf("%s does not document the %q assignee", page, prefix)
			}
		}
	}
	doc := docparity.Read(t, "docs/fleet.md")
	for _, want := range []string{"### Who an assignee names", "no worker, ever", "`assigned_only` still applies"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/fleet.md lacks %q", want)
		}
	}
}

// Every edge case the fleet page states about assignees, run against the
// claim rules.
func TestAssigneeBusinessRules(t *testing.T) {
	cases := []struct {
		name             string
		assignee         string
		profile, crew    string
		assignedOnly     bool
		wantTakes        bool
		wantKind         AssigneeKind
		wantParsedName   string
		wantCleanAllowed bool
	}{
		{"unassigned goes to anyone", "", "builder", "", false, true, AssigneeNone, "", true},
		{"bare name is a profile", "builder", "builder", "", false, true, AssigneeProfile, "builder", true},
		{"bare name for another profile", "reviewer", "builder", "", false, false, AssigneeProfile, "reviewer", true},
		{"a built-in profile keeps its colon", "belai:builder", "belai:builder", "", false, true, AssigneeProfile, "belai:builder", true},
		{"worker: is a profile", "worker:builder", "builder", "", false, true, AssigneeProfile, "builder", true},
		{"worker: escapes a crew-looking name", "worker:crew:x", "crew:x", "", false, true, AssigneeProfile, "crew:x", true},
		{"crew member takes crew", "crew:delivery", "builder", "delivery", false, true, AssigneeCrew, "delivery", true},
		{"lone worker never takes crew", "crew:delivery", "builder", "", false, false, AssigneeCrew, "delivery", true},
		{"other crew never takes crew", "crew:delivery", "builder", "docs", false, false, AssigneeCrew, "delivery", true},
		{"person is never claimed", "person:m-1f2a", "builder", "delivery", false, false, AssigneePerson, "m-1f2a", true},
		{"pending invite is never claimed", "person:invite-3f2b", "builder", "", false, false, AssigneePerson, "invite-3f2b", true},
		{"a space is refused", "crew: delivery", "", "", false, false, AssigneeCrew, " delivery", false},
	}
	for _, c := range cases {
		kind, name := ParseAssignee(c.assignee)
		if kind != c.wantKind || name != c.wantParsedName {
			t.Errorf("%s: ParseAssignee(%q) = %q %q, want %q %q", c.name, c.assignee, kind, name, c.wantKind, c.wantParsedName)
		}
		if got := assigneeTakes(c.assignee, c.profile, c.crew); got != c.wantTakes {
			t.Errorf("%s: assigneeTakes = %v, want %v", c.name, got, c.wantTakes)
		}
		if _, err := CleanAssignee(c.assignee); (err == nil) != c.wantCleanAllowed {
			t.Errorf("%s: CleanAssignee(%q) err = %v, want allowed=%v", c.name, c.assignee, err, c.wantCleanAllowed)
		}
	}
}

// The 64 character cap the docs state, including the prefix.
func TestAssigneeLengthCap(t *testing.T) {
	if _, err := CleanAssignee("crew:" + strings.Repeat("a", 59)); err != nil {
		t.Fatalf("a 64 character assignee was refused: %v", err)
	}
	if _, err := CleanAssignee("crew:" + strings.Repeat("a", 60)); err == nil {
		t.Fatal("a 65 character assignee was accepted")
	}
}

// A profile with assigned_only takes cards assigned to it or to its crew, and
// never an unassigned one, as docs/fleet.md states.
func TestAssignedOnlyTakesOnlyItsOwn(t *testing.T) {
	s := testStore(t)
	addItem(t, s, ItemInput{Title: "unassigned"})
	own := addItem(t, s, ItemInput{Title: "for the crew", Assignee: "crew:delivery"})

	r := claimReq("w1")
	r.AssignedOnly = true
	if _, err := s.Claim(r); !errors.Is(err, ErrNoWork) {
		t.Fatalf("assigned_only worker took an unassigned card: %v", err)
	}
	r.Crew = "delivery"
	got, err := s.Claim(r)
	if err != nil || got.ID != own.ID {
		t.Fatalf("assigned_only crew member missed the crew card: %v %v", got.Title, err)
	}
}
