package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

func bptr(b bool) *bool { return &b }

func TestEligibleAgentsOffersOnlyForegroundDescribedProfilesThatKeepThePosture(t *testing.T) {
	list := []agentprofile.AgentProfile{
		{Name: "mine", Description: "writes release notes", Mode: agentprofile.ModeSingle},
		{Name: "belai:debug", Description: "debug", Mode: agentprofile.ModeSingle, Builtin: true},
		{Name: "loop", Description: "loops", Mode: agentprofile.ModeLoop},
		{Name: "worker", Description: "works", Mode: agentprofile.ModeWorker},
		{Name: "silent", Mode: agentprofile.ModeSingle},
		{Name: "noguard", Description: "x", Mode: agentprofile.ModeSingle, Guardrails: bptr(false)},
		{Name: "noask", Description: "x", Mode: agentprofile.ModeSingle, AskPermission: bptr(false)},
		{Name: "strict", Description: "keeps both", Mode: agentprofile.ModeSingle, Guardrails: bptr(true), AskPermission: bptr(true)},
	}
	got := eligibleAgents(list)
	var names []string
	for _, o := range got {
		names = append(names, o.name)
	}
	if strings.Join(names, ",") != "mine,strict" {
		t.Fatalf("offered %v, want mine,strict", names)
	}
}

func TestEligibleAgentsLeavesRoomForTheDefaultOption(t *testing.T) {
	var list []agentprofile.AgentProfile
	for i := 0; i < 40; i++ {
		list = append(list, agentprofile.AgentProfile{Name: "p" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Description: "d", Mode: agentprofile.ModeSingle})
	}
	if n := len(eligibleAgents(list)); n != jev.MaxPickOptions-1 {
		t.Fatalf("offered %d, want %d", n, jev.MaxPickOptions-1)
	}
}

func TestClearAgentNeedsALeadOverEveryRivalIncludingDefault(t *testing.T) {
	byID := map[string]string{"a1": "release", "a2": "docs"}
	cases := []struct {
		name  string
		probs map[string]float64
		want  string
	}{
		{"clear profile", map[string]float64{"a1": 0.9, "a2": 0.05, "default": 0.05}, "release"},
		{"default wins", map[string]float64{"a1": 0.1, "a2": 0.1, "default": 0.8}, ""},
		{"close rival", map[string]float64{"a1": 0.5, "a2": 0.4, "default": 0.1}, ""},
		{"default as rival", map[string]float64{"a1": 0.6, "a2": 0.0, "default": 0.4}, ""},
		{"below confidence", map[string]float64{"a1": 0.7, "a2": 0.1, "default": 0.2}, ""},
	}
	for _, c := range cases {
		got, ok := clearAgent(c.probs, byID)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: got %q,%v want %q", c.name, got, ok, c.want)
		}
	}
}

// writeProfile stores a user agent profile under the temporary home.
func writeProfile(t *testing.T, name, body string) {
	t.Helper()
	dir, err := agentprofile.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPickAgentEngagesTheClearProfileAndOffersADefault(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	writeProfile(t, "release", `{"name":"release","description":"writes release notes","system_prompt":"You write release notes.","mode":"single"}`)
	d := &pickDecider{probs: map[string]float64{"a1": 0.92, "default": 0.08}}
	s := orderSession(d, nil)
	if got := s.pickAgent(context.Background(), "draft the release notes"); got != "release" {
		t.Fatalf("picked %q, want release", got)
	}
	opts := d.reqs[0].Questions["pick"].Options
	if len(opts) != 2 || opts[len(opts)-1] != jev.AgentDefaultID {
		t.Fatalf("options = %v, want the profile then the default", opts)
	}
	// Only the cleaned request and a description reach the backend, never a
	// raw profile body.
	if strings.Contains(d.reqs[0].Questions["pick"].Descriptions["a1"], "You write release notes.") {
		t.Fatal("the system prompt was sent to the backend")
	}
}

func TestPickAgentRunsWithoutAProfileWhenNothingIsClear(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	writeProfile(t, "release", `{"name":"release","description":"writes release notes","system_prompt":"x","mode":"single"}`)
	d := &pickDecider{probs: map[string]float64{"a1": 0.3, "default": 0.7}}
	if got := orderSession(d, nil).pickAgent(context.Background(), "fix the bug"); got != "" {
		t.Fatalf("picked %q, want none", got)
	}
}

func TestPickAgentIsInertWhenOffUnavailableOrNothingIsOffered(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	d := &pickDecider{probs: map[string]float64{"a1": 1}}
	if got := orderSession(d, nil).pickAgent(context.Background(), "x"); got != "" || d.calls != 0 {
		t.Fatalf("no profiles on offer must not call the backend: %q, %d calls", got, d.calls)
	}
	writeProfile(t, "release", `{"name":"release","description":"writes release notes","system_prompt":"x","mode":"single"}`)
	off := func(j config.JevJob) bool { return j != config.JevAgentPick }
	if got := orderSession(d, off).pickAgent(context.Background(), "x"); got != "" || d.calls != 0 {
		t.Fatalf("the job is off: %q, %d calls", got, d.calls)
	}
	if got := orderSession(nil, nil).pickAgent(context.Background(), "x"); got != "" {
		t.Fatalf("no backend: %q", got)
	}
}
