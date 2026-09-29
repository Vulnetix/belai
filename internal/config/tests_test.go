package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestTestsSettingsDefaults pins the tests block's fail-closed defaults: the
// pass is off unless opted in, the report is off unless opted in, and the
// budgets have sane ceilings when unset.
func TestTestsSettingsDefaults(t *testing.T) {
	var unset Settings
	if unset.TestsPostEnd() != "off" {
		t.Errorf("TestsPostEnd unset = %q, want off", unset.TestsPostEnd())
	}
	if unset.TestsEnabled() {
		t.Error("TestsEnabled unset must be false")
	}
	if unset.TestsScope() != "affected" {
		t.Errorf("TestsScope unset = %q, want affected", unset.TestsScope())
	}
	if unset.TestsOnFail() != "fix" {
		t.Errorf("TestsOnFail unset = %q, want fix", unset.TestsOnFail())
	}
	if unset.TestsMaxFixPasses() != DefaultTestsMaxFixPasses {
		t.Errorf("TestsMaxFixPasses unset = %d, want %d", unset.TestsMaxFixPasses(), DefaultTestsMaxFixPasses)
	}
	if unset.TestsTimeoutSeconds() != DefaultTestsTimeoutSeconds {
		t.Errorf("TestsTimeoutSeconds unset = %d, want %d", unset.TestsTimeoutSeconds(), DefaultTestsTimeoutSeconds)
	}
	if !unset.TestsReportEnabled() {
		t.Error("TestsReportEnabled unset must be true")
	}
	off := false
	if (Settings{Tests: &TestsSettings{Report: &off}}).TestsReportEnabled() {
		t.Error("TestsReportEnabled with explicit false must be false")
	}
	if unset.TestsCommand() != nil {
		t.Errorf("TestsCommand unset = %v, want nil", unset.TestsCommand())
	}
}

func TestTestsSettingsRoundTrip(t *testing.T) {
	on := true
	s := Settings{Tests: &TestsSettings{
		PostEnd:        "goal_plan",
		Command:        []string{"go", "test", "./..."},
		Scope:          "full",
		OnFail:         "diagnose",
		MaxFixPasses:   7,
		TimeoutSeconds: 120,
		Report:         &on,
	}}
	if !s.TestsEnabled() {
		t.Error("TestsEnabled with goal_plan must be true")
	}
	if got := s.TestsPostEnd(); got != "goal_plan" {
		t.Errorf("TestsPostEnd = %q, want goal_plan", got)
	}
	if got := s.TestsCommand(); !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Errorf("TestsCommand = %v", got)
	}
	if got := s.TestsScope(); got != "full" {
		t.Errorf("TestsScope = %q, want full", got)
	}
	if got := s.TestsOnFail(); got != "diagnose" {
		t.Errorf("TestsOnFail = %q, want diagnose", got)
	}
	if got := s.TestsMaxFixPasses(); got != 7 {
		t.Errorf("TestsMaxFixPasses = %d, want 7", got)
	}
	if got := s.TestsTimeoutSeconds(); got != 120 {
		t.Errorf("TestsTimeoutSeconds = %d, want 120", got)
	}
	if !s.TestsReportEnabled() {
		t.Error("TestsReportEnabled with explicit true must be true")
	}
}

// TestTestsProjectDrop pins the asymmetry: a repo-visible project layer may
// turn the pass off and lower budgets, never on, never change the command,
// scope, on_fail or report.
func TestTestsProjectDrop(t *testing.T) {
	on := true
	global := Settings{Tests: &TestsSettings{
		PostEnd:        "session",
		Command:        []string{"go", "test", "./..."},
		Scope:          "affected",
		OnFail:         "fix",
		MaxFixPasses:   5,
		TimeoutSeconds: 300,
		Report:         &on,
	}}

	// Project turns it off: the pass must not run.
	off := global.Override(Settings{Tests: &TestsSettings{PostEnd: "off"}})
	if off.TestsEnabled() {
		t.Fatal("project post_end off must disable the pass")
	}

	// Project post_end session must be dropped (never turn on).
	keptOff := Settings{}.Override(Settings{Tests: &TestsSettings{PostEnd: "session"}})
	if keptOff.TestsEnabled() {
		t.Fatal("project layer must never turn the pass on")
	}

	// Project command/scope/on_fail/report are dropped.
	proj := global.Override(Settings{Tests: &TestsSettings{
		Command:        []string{"sh", "-c", "evil"},
		Scope:          "full",
		OnFail:         "off",
		MaxFixPasses:   99,
		TimeoutSeconds: 999,
		Report:         &on,
	}})
	if got := proj.TestsCommand(); !reflect.DeepEqual(got, global.TestsCommand()) {
		t.Errorf("project command leaked: %v", got)
	}
	if got := proj.TestsScope(); got != "affected" {
		t.Errorf("project scope widened to %q", got)
	}
	if got := proj.TestsOnFail(); got != "fix" {
		t.Errorf("project on_fail changed to %q", got)
	}
	if got := proj.TestsMaxFixPasses(); got != 5 {
		t.Errorf("project max_fix_passes widened to %d", got)
	}
	if got := proj.TestsTimeoutSeconds(); got != 300 {
		t.Errorf("project timeout_seconds widened to %d", got)
	}

	// Project may lower the budgets.
	lower := global.Override(Settings{Tests: &TestsSettings{MaxFixPasses: 2, TimeoutSeconds: 60}})
	if got := lower.TestsMaxFixPasses(); got != 2 {
		t.Errorf("project lower max_fix_passes = %d, want 2", got)
	}
	if got := lower.TestsTimeoutSeconds(); got != 60 {
		t.Errorf("project lower timeout_seconds = %d, want 60", got)
	}
}

// TestTestsUserMerge pins that a user layer controls every key outright. The
// user path is Effective.apply (non-project sources), exercised directly here
// because it is the only code path that merges tests for the user's layers.
func TestTestsUserMerge(t *testing.T) {
	eff := Effective{Settings: Settings{Tests: &TestsSettings{PostEnd: "goal", MaxFixPasses: 3}}, Origin: map[string]Source{}}
	eff.apply(Settings{Tests: &TestsSettings{
		PostEnd: "goal_plan",
		Command: []string{"just", "check"},
		Scope:   "full",
		OnFail:  "off",
	}}, SourceGlobal)

	got := eff.Settings
	if got.TestsPostEnd() != "goal_plan" {
		t.Errorf("user post_end = %q, want goal_plan", got.TestsPostEnd())
	}
	if c := got.TestsCommand(); !reflect.DeepEqual(c, []string{"just", "check"}) {
		t.Errorf("user command = %v", c)
	}
	if got.TestsScope() != "full" {
		t.Errorf("user scope = %q, want full", got.TestsScope())
	}
	if got.TestsOnFail() != "off" {
		t.Errorf("user on_fail = %q, want off", got.TestsOnFail())
	}
	if got.TestsMaxFixPasses() != 3 {
		t.Errorf("max_fix_passes inherited = %d, want 3", got.TestsMaxFixPasses())
	}
}

// An unrecognised value never widens the pass: post_end is off, on_fail is
// off, and scope is the default.
func TestTestsUnknownValuesFailClosed(t *testing.T) {
	s := Settings{Tests: &TestsSettings{PostEnd: "always", OnFail: "yolo", Scope: "everything"}}
	if s.TestsEnabled() || s.TestsPostEnd() != "always" {
		t.Errorf("post_end always: enabled=%v level=%q", s.TestsEnabled(), s.TestsPostEnd())
	}
	if got := s.TestsOnFail(); got != "off" {
		t.Errorf("TestsOnFail(yolo) = %q, want off", got)
	}
	if got := s.TestsScope(); got != "affected" {
		t.Errorf("TestsScope(everything) = %q, want affected", got)
	}
	if got := (Settings{Tests: &TestsSettings{Scope: "full"}}).TestsScope(); got != "full" {
		t.Errorf("TestsScope(full) = %q", got)
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// A cloned repository's tests block cannot turn the pass on or pick a command,
// and Resolve says so.
func TestResolveDropsProjectTestsWideningWithANote(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveProject(workdir, Settings{Tests: &TestsSettings{PostEnd: "session", Command: []string{"sh", "-c", "evil"}}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.TestsEnabled() || eff.Settings.TestsCommand() != nil {
		t.Fatalf("project tests block took effect: %+v", eff.Settings.Tests)
	}
	if !hasNote(eff.Notes, "project tests settings ignored") {
		t.Fatalf("no drop note: %v", eff.Notes)
	}
}

// Turning the pass off or lowering a budget from the project is allowed and
// draws no note.
func TestResolveProjectTestsTighteningDrawsNoNote(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{Tests: &TestsSettings{PostEnd: "goal"}}); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}
	if err := SaveProject(workdir, Settings{Tests: &TestsSettings{PostEnd: "off", MaxFixPasses: 1}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if eff.Settings.TestsEnabled() || eff.Settings.TestsMaxFixPasses() != 1 {
		t.Fatalf("project tightening lost: %+v", eff.Settings.Tests)
	}
	if hasNote(eff.Notes, "project tests settings ignored") {
		t.Fatalf("tightening drew a note: %v", eff.Notes)
	}
}
