package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommitModeEffective(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		s    Settings
		want string
	}{
		{"unset", Settings{}, "off"},
		{"explicit off", Settings{CommitMode: "off"}, "off"},
		{"ask", Settings{CommitMode: "ask"}, "ask"},
		{"auto", Settings{CommitMode: "auto"}, "auto"},
		{"case and space", Settings{CommitMode: " Auto "}, "auto"},
		{"legacy true", Settings{AutoCommitPerTask: &yes}, "auto"},
		{"legacy false", Settings{AutoCommitPerTask: &no}, "off"},
		{"explicit beats legacy", Settings{CommitMode: "ask", AutoCommitPerTask: &yes}, "ask"},
		{"explicit off beats legacy", Settings{CommitMode: "off", AutoCommitPerTask: &yes}, "off"},
		{"unknown is off", Settings{CommitMode: "always", AutoCommitPerTask: &yes}, "off"},
	}
	for _, tc := range cases {
		if got := tc.s.CommitModeEffective(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if !slices.Equal(CommitModeNames, []string{"off", "ask", "auto"}) {
		t.Fatalf("CommitModeNames = %v", CommitModeNames)
	}
}

func TestValidateCommitWorkflow(t *testing.T) {
	ok := Settings{CommitMode: "ask", CommitExclude: []string{"*.log", "build/", "docs/gen", "a/*/b"}}
	if err := ValidateCommitWorkflow(ok); err != nil {
		t.Fatal(err)
	}
	bad := []Settings{
		{CommitMode: "sometimes"},
		{CommitExclude: []string{""}},
		{CommitExclude: []string{"/etc/passwd"}},
		{CommitExclude: []string{"../up"}},
		{CommitExclude: []string{"a/../b"}},
		{CommitExclude: []string{"a//b"}},
		{CommitExclude: []string{"a\\b"}},
		{CommitExclude: []string{"x\x00y"}},
		{CommitExclude: []string{"["}},
		{CommitExclude: []string{strings.Repeat("a", MaxCommitExcludeLength+1)}},
		{CommitExclude: make([]string, MaxCommitExclude+1)},
	}
	for i, s := range bad {
		if len(s.CommitExclude) > MaxCommitExclude {
			for j := range s.CommitExclude {
				s.CommitExclude[j] = "x"
			}
		}
		if err := ValidateCommitWorkflow(s); err == nil {
			t.Errorf("case %d (%+v) was accepted", i, s)
		}
	}
}

func TestResolveDropsProjectCommitModeAndDraft(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := SaveProject(workdir, Settings{CommitMode: "auto", CommitDraft: &on}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.CommitModeEffective() != "off" || eff.Settings.CommitDraftEnabled() {
		t.Fatalf("a project layer turned the commit workflow on: %+v", eff.Settings)
	}
	for _, key := range []string{"commit_mode", "commit_draft"} {
		if eff.Origin[key] == SourceProject {
			t.Errorf("project must not claim %s", key)
		}
	}
	notes := strings.Join(eff.Notes, "\n")
	if !strings.Contains(notes, "project commit_mode ignored") || !strings.Contains(notes, "project commit_draft ignored") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestResolveProjectCannotWidenTheLegacySwitchEither(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := SaveProject(workdir, Settings{AutoCommitPerTask: &on}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.CommitModeEffective() != "off" {
		t.Fatal("a project layer turned the legacy switch on")
	}
}

func TestResolveGlobalCommitModeAndDraftSurviveAProject(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	if err := SaveGlobal(Settings{CommitMode: "ask", CommitDraft: &on}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{CommitMode: "off", CommitDraft: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Settings.CommitModeEffective() != "ask" || !eff.Settings.CommitDraftEnabled() {
		t.Fatalf("a project layer changed the user's choice: %+v", eff.Settings)
	}
	if eff.Origin["commit_mode"] != SourceGlobal {
		t.Fatalf("origin = %v", eff.Origin["commit_mode"])
	}
}

func TestProjectCommitExcludeOnlyAdds(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	if err := SaveGlobal(Settings{CommitExclude: []string{"*.log"}}); err != nil {
		t.Fatal(err)
	}
	proj := []string{"*.tmp", "*.log", "../escape", "/abs", "["}
	if err := SaveProject(workdir, Settings{CommitExclude: proj}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("a hostile project pattern must be dropped, not stop startup: %v", err)
	}
	if got := eff.Settings.CommitExcludeGlobs(); !slices.Equal(got, []string{"*.log", "*.tmp"}) {
		t.Fatalf("exclusions = %v", got)
	}
	if !strings.Contains(strings.Join(eff.Notes, "\n"), "3 pattern(s) ignored") {
		t.Fatalf("notes = %v", eff.Notes)
	}
	// The returned list is a copy.
	eff.Settings.CommitExcludeGlobs()[0] = "changed"
	if eff.Settings.CommitExclude[0] != "*.log" {
		t.Fatal("CommitExcludeGlobs shares storage")
	}
}

func TestProjectCommitExcludeRespectsTheCount(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	var user, proj []string
	for i := 0; i < MaxCommitExclude-2; i++ {
		user = append(user, "u"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	for i := 0; i < 10; i++ {
		proj = append(proj, "p"+string(rune('a'+i)))
	}
	if err := SaveGlobal(Settings{CommitExclude: user}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{CommitExclude: proj}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(eff.Settings.CommitExclude); n != MaxCommitExclude {
		t.Fatalf("exclusions = %d, want the cap %d", n, MaxCommitExclude)
	}
}

func TestCommitSettingsRoundTripAndAreNotPrefKeys(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	on := true
	if err := SaveGlobal(Settings{CommitMode: "ask", CommitDraft: &on, CommitExclude: []string{"*.log"}}); err != nil {
		t.Fatal(err)
	}
	path, _ := GlobalSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"commit_mode": "ask"`, `"commit_draft": true`, `"commit_exclude"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings file lacks %s:\n%s", want, data)
		}
	}
	// The web can set a project preference; it must not be able to set these.
	for _, k := range PrefKeys() {
		if strings.HasPrefix(k, "commit_") {
			t.Errorf("%s is a web project preference", k)
		}
	}
}

func TestProjectCommitsDir(t *testing.T) {
	got := ProjectCommitsDir("/w")
	if got != filepath.Join("/w", ".vulnetix", "belai", "commits") {
		t.Fatalf("got %s", got)
	}
}
