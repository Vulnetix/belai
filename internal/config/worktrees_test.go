package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestWorktreeDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Worktrees: &WorktreeSettings{}}} {
		if s.WorktreeSetupArgv() != nil || s.WorktreeTeardownArgv() != nil || s.WorktreeTimeout() != 120 ||
			s.WorktreeUpdate() != WorktreeUpdateOff || !s.WorktreeKeepOnExit() || s.WorktreeNetworkAllowed() {
			t.Fatalf("defaults wrong for %+v", s.Worktrees)
		}
	}
}

func TestWorktreeTimeoutIsClamped(t *testing.T) {
	for in, want := range map[int]int{-3: 1, 0: 1, 1: 1, 120: 120, 600: 600, 9000: 600} {
		s := Settings{Worktrees: &WorktreeSettings{TimeoutSeconds: ip(in)}}
		if got := s.WorktreeTimeout(); got != want {
			t.Errorf("timeout_seconds %d -> %d, want %d", in, got, want)
		}
	}
}

func TestWorktreeUnknownValuesFailClosed(t *testing.T) {
	s := Settings{Worktrees: &WorktreeSettings{UpdateMode: sp("force"), Network: sp("maybe")}}
	if s.WorktreeUpdate() != WorktreeUpdateOff || s.WorktreeNetworkAllowed() {
		t.Fatalf("an unknown value must read as off and deny: %+v", s.Worktrees)
	}
	if err := ValidateWorktrees(s); err == nil {
		t.Fatal("an unknown update_mode validated")
	}
}

func TestValidWorktreeArgv(t *testing.T) {
	good := [][]string{
		{"npm", "ci"},
		{"./scripts/setup.sh", "--quiet"},
		{"/usr/bin/env", "true"},
		{"go", "mod", "download", "$HOME is just text"},
	}
	for _, a := range good {
		if err := ValidWorktreeArgv(a); err != nil {
			t.Errorf("%q refused: %v", a, err)
		}
	}
	long := strings.Repeat("a", MaxWorktreeArgBytes+1)
	bad := map[string][]string{
		"empty":        {},
		"blank prog":   {""},
		"padded":       {" npm"},
		"shell prog":   {"sh -c"},
		"metachar":     {"npm;rm"},
		"dash prog":    {"-rf"},
		"climbs":       {"../x.sh"},
		"climbs mid":   {"./a/../../x.sh"},
		"control word": {"npm", "a\x00b"},
		"newline":      {"npm", "a\nb"},
		"long word":    {"npm", long},
		"too many":     append([]string{"npm"}, make([]string, MaxWorktreeArgs)...),
	}
	for name, a := range bad {
		if err := ValidWorktreeArgv(a); err == nil {
			t.Errorf("%s: %q validated", name, a)
		}
	}
}

func TestValidateWorktreesBounds(t *testing.T) {
	tooMany := make([][]string, MaxWorktreeCommands+1)
	for i := range tooMany {
		tooMany[i] = []string{"true"}
	}
	for name, w := range map[string]*WorktreeSettings{
		"setup count":    {Setup: tooMany},
		"teardown count": {Teardown: tooMany},
		"setup argv":     {Setup: [][]string{{"a b"}}},
		"timeout low":    {TimeoutSeconds: ip(0)},
		"timeout high":   {TimeoutSeconds: ip(601)},
		"network":        {Network: sp("open")},
	} {
		if err := ValidateWorktrees(Settings{Worktrees: w}); err == nil {
			t.Errorf("%s validated", name)
		}
	}
	ok := &WorktreeSettings{Setup: [][]string{{"npm", "ci"}}, Teardown: [][]string{{"true"}}, TimeoutSeconds: ip(600), UpdateMode: sp("Rebase"), Network: sp("ALLOW")}
	if err := ValidateWorktrees(Settings{Worktrees: ok}); err != nil {
		t.Fatal(err)
	}
	s := Settings{Worktrees: ok}
	if s.WorktreeUpdate() != WorktreeUpdateRebase || !s.WorktreeNetworkAllowed() {
		t.Fatalf("case was not folded: %+v", s.Worktrees)
	}
}

func TestWorktreeAccessorsCopy(t *testing.T) {
	s := Settings{Worktrees: &WorktreeSettings{Setup: [][]string{{"npm", "ci"}}}}
	s.WorktreeSetupArgv()[0][0] = "evil"
	if s.Worktrees.Setup[0][0] != "npm" {
		t.Fatal("the accessor handed out the settings' own slice")
	}
}

func TestWorktreeOverrideNarrowsOnly(t *testing.T) {
	user := Settings{Worktrees: &WorktreeSettings{Setup: [][]string{{"npm", "ci"}}, UpdateMode: sp("merge"), TimeoutSeconds: ip(30), KeepOnExit: bp(false)}}
	out := user.Override(Settings{Worktrees: &WorktreeSettings{
		Setup: [][]string{{"curl", "evil"}}, Teardown: [][]string{{"rm"}}, TimeoutSeconds: ip(600),
		UpdateMode: sp("rebase"), KeepOnExit: bp(true), Network: sp("allow"),
	}})
	if got := out.WorktreeSetupArgv(); len(got) != 1 || got[0][0] != "npm" || out.WorktreeTeardownArgv() != nil ||
		out.WorktreeTimeout() != 30 || out.WorktreeUpdate() != WorktreeUpdateMerge || out.WorktreeKeepOnExit() || out.WorktreeNetworkAllowed() {
		t.Fatalf("a project widened the worktree settings: %+v", out.Worktrees)
	}
	out = user.Override(Settings{Worktrees: &WorktreeSettings{UpdateMode: sp("off")}})
	if out.WorktreeUpdate() != WorktreeUpdateOff {
		t.Fatalf("a project could not turn update_mode off: %+v", out.Worktrees)
	}
	// Nothing from a project creates the block from nothing.
	if got := (Settings{}).Override(Settings{Worktrees: &WorktreeSettings{Setup: [][]string{{"x"}}}}); got.Worktrees != nil {
		t.Fatalf("a project created the worktrees block: %+v", got.Worktrees)
	}
}

func TestWorktreeJSONRoundTrip(t *testing.T) {
	var s Settings
	raw := `{"worktrees":{"setup":[["npm","ci"]],"teardown":[["true"]],"timeout_seconds":45,"update_mode":"rebase","keep_on_exit":false,"network":"allow"}}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.WorktreeSetupArgv()) != 1 || len(s.WorktreeTeardownArgv()) != 1 || s.WorktreeTimeout() != 45 ||
		s.WorktreeUpdate() != WorktreeUpdateRebase || s.WorktreeKeepOnExit() || !s.WorktreeNetworkAllowed() {
		t.Fatalf("%+v", s.Worktrees)
	}
}

func TestResolveWorktreesProjectLayer(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	workdir := t.TempDir()
	resolve := func() Effective {
		t.Helper()
		eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
		if err != nil {
			t.Fatal(err)
		}
		return eff
	}
	if err := SaveGlobal(Settings{Worktrees: &WorktreeSettings{Setup: [][]string{{"npm", "ci"}}, UpdateMode: sp("merge")}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Worktrees: &WorktreeSettings{
		Setup: [][]string{{"curl", "https://evil.example"}}, Teardown: [][]string{{"rm", "-rf"}},
		TimeoutSeconds: ip(600), UpdateMode: sp("rebase"), KeepOnExit: bp(false), Network: sp("allow"),
	}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	s := eff.Settings
	if got := s.WorktreeSetupArgv(); len(got) != 1 || got[0][0] != "npm" || s.WorktreeTeardownArgv() != nil ||
		s.WorktreeTimeout() != 120 || s.WorktreeUpdate() != WorktreeUpdateMerge || !s.WorktreeKeepOnExit() || s.WorktreeNetworkAllowed() {
		t.Fatalf("project widened: %+v", s.Worktrees)
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "worktrees.setup") && strings.Contains(n, "worktrees.teardown") && strings.Contains(n, "worktrees.network") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	// A project may narrow update_mode to off.
	if err := SaveProject(workdir, Settings{Worktrees: &WorktreeSettings{UpdateMode: sp("off")}}); err != nil {
		t.Fatal(err)
	}
	if got := resolve(); got.Settings.WorktreeUpdate() != WorktreeUpdateOff || len(got.Settings.WorktreeSetupArgv()) != 1 {
		t.Fatalf("a project could not turn update_mode off: %+v", got.Settings.Worktrees)
	}
}

func TestWorktreeStateDir(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir, err := WorktreeStateDir(WorkdirKey("/some/repo"))
	if err != nil || !strings.Contains(dir, "worktrees") {
		t.Fatalf("%q %v", dir, err)
	}
	for _, bad := range []string{"", "..", "../x", "a/b", ".hidden"} {
		if _, err := WorktreeStateDir(bad); err == nil {
			t.Errorf("project key %q accepted", bad)
		}
	}
}

// TestWorktreesPageStatesEachDefaultAndRange keeps docs/worktrees.md equal to
// the bounds the code clamps to.
func TestWorktreesPageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/worktrees.md")
	for _, want := range []string{
		"| `worktrees.setup` | none |",
		"| `worktrees.teardown` | none |",
		fmt.Sprintf("| `worktrees.timeout_seconds` | %d | 1 to %d |", DefaultWorktreeTimeoutSeconds, MaxWorktreeTimeoutSeconds),
		"| `worktrees.update_mode` | off | off, merge or rebase |",
		"| `worktrees.keep_on_exit` | on |",
		"| `worktrees.network` | deny | deny or allow |",
		fmt.Sprintf("at most %d commands", MaxWorktreeCommands),
		fmt.Sprintf("%d words", MaxWorktreeArgs),
		fmt.Sprintf("%d bytes", MaxWorktreeArgBytes),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/worktrees.md does not state %q", want)
		}
	}
}
