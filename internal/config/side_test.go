package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestSideDefaults(t *testing.T) {
	for _, s := range []Settings{{}, {Side: &SideSettings{}}} {
		if !s.SideEnabled() || s.SideHistoryTurns() != 6 || s.SideMaxReplyBytes() != 4096 {
			t.Fatalf("defaults wrong for %+v", s.Side)
		}
	}
}

func TestSideLimitsAreClamped(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 6: 6, 20: 20, 21: 20, 500: 20} {
		s := Settings{Side: &SideSettings{HistoryTurns: ip(in)}}
		if got := s.SideHistoryTurns(); got != want {
			t.Errorf("history_turns %d -> %d, want %d", in, got, want)
		}
	}
	for in, want := range map[int]int{-1: 512, 0: 512, 511: 512, 512: 512, 4096: 4096, 8192: 8192, 9000: 8192} {
		s := Settings{Side: &SideSettings{MaxReplyBytes: ip(in)}}
		if got := s.SideMaxReplyBytes(); got != want {
			t.Errorf("max_reply_bytes %d -> %d, want %d", in, got, want)
		}
		if err := ValidateSettings(s); err != nil {
			t.Errorf("max_reply_bytes %d failed validation: %v", in, err)
		}
	}
}

func TestSideUserLayerSetsAnything(t *testing.T) {
	got := mergeSide(&SideSettings{Enabled: bp(false), HistoryTurns: ip(1), MaxReplyBytes: ip(600)},
		&SideSettings{Enabled: bp(true), HistoryTurns: ip(20), MaxReplyBytes: ip(8192)}, false)
	s := Settings{Side: got}
	if !s.SideEnabled() || s.SideHistoryTurns() != 20 || s.SideMaxReplyBytes() != 8192 {
		t.Fatalf("user layer not honoured: %+v", got)
	}
}

func TestSideProjectCanOnlyNarrow(t *testing.T) {
	user := &SideSettings{Enabled: bp(false), HistoryTurns: ip(3), MaxReplyBytes: ip(1000)}
	got := mergeSide(user, &SideSettings{Enabled: bp(true), HistoryTurns: ip(20), MaxReplyBytes: ip(8192)}, true)
	s := Settings{Side: got}
	if s.SideEnabled() || s.SideHistoryTurns() != 3 || s.SideMaxReplyBytes() != 1000 {
		t.Fatalf("a project widened: %+v", got)
	}
	// Against the defaults a project still cannot set the switch true or
	// store an equal or higher limit.
	got = mergeSide(nil, &SideSettings{Enabled: bp(true), HistoryTurns: ip(6), MaxReplyBytes: ip(4096)}, true)
	if got.Enabled != nil || got.HistoryTurns != nil || got.MaxReplyBytes != nil {
		t.Fatalf("a no-op or widening project value was stored: %+v", got)
	}
	// Turning off and lowering apply; a value below the floor is clamped to it.
	got = mergeSide(nil, &SideSettings{Enabled: bp(false), HistoryTurns: ip(-4), MaxReplyBytes: ip(10)}, true)
	s = Settings{Side: got}
	if s.SideEnabled() || s.SideHistoryTurns() != 0 || s.SideMaxReplyBytes() != 512 {
		t.Fatalf("a project could not narrow: %+v", got)
	}
}

func TestSideOverrideNarrowsOnly(t *testing.T) {
	user := Settings{Side: &SideSettings{MaxReplyBytes: ip(6000)}}
	out := user.Override(Settings{Side: &SideSettings{MaxReplyBytes: ip(8000), HistoryTurns: ip(2), Enabled: bp(true)}})
	if out.SideMaxReplyBytes() != 6000 || out.SideHistoryTurns() != 2 || !out.SideEnabled() {
		t.Fatalf("Override: %+v", out.Side)
	}
	out = user.Override(Settings{Side: &SideSettings{Enabled: bp(false)}})
	if out.SideEnabled() {
		t.Fatal("Override did not turn /btw off")
	}
}

func TestSideJSONRoundTrip(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"side":{"enabled":false,"history_turns":2,"max_reply_bytes":1024}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.SideEnabled() || s.SideHistoryTurns() != 2 || s.SideMaxReplyBytes() != 1024 {
		t.Fatalf("%+v", s.Side)
	}
}

func TestResolveSideProjectLayer(t *testing.T) {
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
	if err := SaveGlobal(Settings{Side: &SideSettings{Enabled: bp(false), HistoryTurns: ip(2), MaxReplyBytes: ip(1024)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Side: &SideSettings{Enabled: bp(true), HistoryTurns: ip(20), MaxReplyBytes: ip(8192)}}); err != nil {
		t.Fatal(err)
	}
	eff := resolve()
	if eff.Settings.SideEnabled() || eff.Settings.SideHistoryTurns() != 2 || eff.Settings.SideMaxReplyBytes() != 1024 {
		t.Fatalf("project widened: %+v", eff.Settings.Side)
	}
	noted := false
	for _, n := range eff.Notes {
		if strings.Contains(n, "side.enabled") && strings.Contains(n, "side.history_turns") && strings.Contains(n, "side.max_reply_bytes") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no note for the ignored project keys: %v", eff.Notes)
	}
	// A user who left the defaults: a project lowers both limits.
	if err := SaveGlobal(Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Side: &SideSettings{HistoryTurns: ip(1), MaxReplyBytes: ip(700)}}); err != nil {
		t.Fatal(err)
	}
	eff = resolve()
	if eff.Settings.SideHistoryTurns() != 1 || eff.Settings.SideMaxReplyBytes() != 700 || eff.Origin["side"] != SourceProject {
		t.Fatalf("project narrowing not applied: %+v origin %v", eff.Settings.Side, eff.Origin["side"])
	}
	// A project turns /btw off, and cannot turn it back on over a user's off.
	if err := SaveProject(workdir, Settings{Side: &SideSettings{Enabled: bp(false)}}); err != nil {
		t.Fatal(err)
	}
	if resolve().Settings.SideEnabled() {
		t.Fatal("a project could not turn /btw off")
	}
	if err := SaveGlobal(Settings{Side: &SideSettings{Enabled: bp(false)}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{Side: &SideSettings{Enabled: bp(true)}}); err != nil {
		t.Fatal(err)
	}
	if resolve().Settings.SideEnabled() {
		t.Fatal("a project turned /btw on")
	}
}

// TestSidePageStatesEachDefaultAndRange keeps docs/side-question.md equal to
// the bounds the code clamps to.
func TestSidePageStatesEachDefaultAndRange(t *testing.T) {
	doc := docparity.Read(t, "docs/side-question.md")
	for _, want := range []string{
		"| `side.enabled` | on |",
		fmt.Sprintf("| `side.history_turns` | %d | 0 to %d |", DefaultSideHistoryTurns, MaxSideHistoryTurns),
		fmt.Sprintf("| `side.max_reply_bytes` | %d | %d to %d |", DefaultSideMaxReplyBytes, MinSideMaxReplyBytes, MaxSideMaxReplyBytes),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/side-question.md does not state %q", want)
		}
	}
}
