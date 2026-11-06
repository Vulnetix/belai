package config

import (
	"strings"
	"testing"
)

func TestReplyCostDefaultsOffAndProjectCanOnlyTurnItOff(t *testing.T) {
	var s Settings
	if s.ReplyCostEnabled() {
		t.Fatal("default must be off")
	}
	on := Settings{UI: &UISettings{ReplyCost: ptr(true)}}
	if !on.ReplyCostEnabled() {
		t.Fatal("a user layer must be able to turn it on")
	}

	// Override (the project fold) drops a true and keeps a false.
	if merged := (Settings{}).Override(Settings{UI: &UISettings{ReplyCost: ptr(true), Colors: ptr(false)}}); merged.ReplyCostEnabled() || merged.ColorsEnabled() {
		t.Fatalf("a project layer turned the reply cost on, or lost a sibling key: %+v", merged.UI)
	}
	if merged := on.Override(Settings{UI: &UISettings{ReplyCost: ptr(false)}}); merged.ReplyCostEnabled() {
		t.Fatal("a project false must win over a user true")
	}

	// The same rule through Resolve's layer fold, with the note.
	e := &Effective{Origin: map[string]Source{}}
	e.apply(Settings{UI: &UISettings{ReplyCost: ptr(true)}}, SourceProject)
	if e.Settings.ReplyCostEnabled() {
		t.Fatal("Resolve let a project layer turn the reply cost on")
	}
	var noted bool
	for _, n := range e.Notes {
		noted = noted || strings.Contains(n, "project ui.reply_cost ignored")
	}
	if !noted {
		t.Fatalf("no note for the dropped project value: %v", e.Notes)
	}
	e.apply(Settings{UI: &UISettings{ReplyCost: ptr(true)}}, SourceGlobal)
	if !e.Settings.ReplyCostEnabled() {
		t.Fatal("a user layer must be able to turn it on")
	}
	n := len(e.Notes)
	e.apply(Settings{UI: &UISettings{ReplyCost: ptr(false)}}, SourceProject)
	if e.Settings.ReplyCostEnabled() || len(e.Notes) != n {
		t.Fatalf("a project false must apply without a note: %v", e.Notes)
	}
}
