package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/notify"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/promptlib"
	"github.com/vulnetix/belai/internal/tools"
)

func TestCleanLogKeepsTextAndDropsControls(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain text is kept", "build ok", "build ok"},
		{"ANSI styling collapses to its text", "\x1b[1;31merror\x1b[0m: boom", "error: boom"},
		{"newline and tab survive", "a\tb\nc", "a\tb\nc"},
		{"C0 controls and DEL are dropped", "a\x00b\x07c\rd\x7fe", "abcde"},
		{"C1 controls are dropped", "a\u0085b\u009bc", "abc"},
		{"bidi embeddings and overrides are dropped", "a‪b‮c⁦d⁩e", "abcde"},
		{"direction marks are dropped", "a‎b‏c؜d", "abcd"},
		{"other non-ASCII text is kept", "naïve → ok", "naïve → ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanLog(tc.in); got != tc.want {
				t.Fatalf("cleanLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateIsRuneSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short string is unchanged", "abc", 5, "abc"},
		{"exactly n runes is unchanged", "abcde", 5, "abcde"},
		{"longer string keeps n-1 runes and an ellipsis", "abcdef", 5, "abcd…"},
		{"multi-byte runes are never split", "日本語のテキスト", 4, "日本語…"},
		{"empty string", "", 3, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if n := len([]rune(got)); n > tc.n {
				t.Fatalf("truncate(%q, %d) has %d runes", tc.in, tc.n, n)
			}
		})
	}
}

func TestParseTargetCutsProviderAtTheFirstSlash(t *testing.T) {
	cases := map[string]fleet.Target{
		"openai/gpt-5":                    {Provider: "openai", Model: "gpt-5"},
		"openrouter/deepseek/deepseek-v4": {Provider: "openrouter", Model: "deepseek/deepseek-v4"},
		"gpt-5":                           {Model: "gpt-5"},
		"":                                {},
	}
	for in, want := range cases {
		if got := parseTarget(in); got != want {
			t.Errorf("parseTarget(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestBoolCode(t *testing.T) {
	if got := boolCode(nil); got != 0 {
		t.Errorf("boolCode(nil) = %d, want 0", got)
	}
	if got := boolCode(errors.New("boom")); got != 1 {
		t.Errorf("boolCode(err) = %d, want 1", got)
	}
}

func TestCLITags(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).UnixMilli()
	minute := time.Minute.Milliseconds()
	cases := []struct {
		name string
		item kanban.Item
		want string
	}{
		{"an untagged item has no tags", kanban.Item{Title: "x"}, ""},
		{
			"every tag, in order, with an active lease rounded to the minute",
			kanban.Item{
				Labels: []string{"build", "go"}, Priority: 2, Assignee: "builder",
				ClaimedBy: "worker-1", LeaseUntil: now + 14*minute + 20_000,
				Branch: "fix/1-thing", PR: "https://github.com/o/r/pull/1",
			},
			"[build,go]  p2  @builder  claimed by worker-1 (14m0s)  fix/1-thing  https://github.com/o/r/pull/1",
		},
		{"a lease in the past has lapsed", kanban.Item{ClaimedBy: "w", LeaseUntil: now - minute}, "claimed by w (lapsed)"},
		{"a lease ending now has lapsed", kanban.Item{ClaimedBy: "w", LeaseUntil: now}, "claimed by w (lapsed)"},
		{"a negative priority is shown", kanban.Item{Priority: -1}, "p-1"},
		{"a lease without a claimant is not shown", kanban.Item{LeaseUntil: now + minute}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliTags(tc.item, now); got != tc.want {
				t.Fatalf("cliTags = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrintJSONWritesKanbanItemsInTheirWireForm(t *testing.T) {
	it := kanban.Item{
		ID: "11111111-2222-4333-8444-555555555555", Title: "Fix the flake", List: kanban.List("doing"),
		Labels: []string{"test"}, Priority: 1, ClaimedBy: "worker-1", Branch: "fix/flake", Dir: "/work/repo",
	}

	var one bytes.Buffer
	if err := printJSON(&one, it); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(one.String(), "{\n  \"id\": ") {
		t.Fatalf("an item is not indented two spaces:\n%s", one.String())
	}
	checkWire := func(t *testing.T, m map[string]any) {
		t.Helper()
		if m["id"] != it.ID || m["title"] != it.Title || m["list"] != "doing" || m["cwd"] != "/work/repo" {
			t.Fatalf("wire item = %v", m)
		}
		agent, ok := m["agent"].(map[string]any)
		if !ok {
			t.Fatalf("wire item has no agent block: %v", m)
		}
		if agent["claimedBy"] != "worker-1" || agent["branch"] != "fix/flake" || agent["priority"] != float64(1) {
			t.Fatalf("agent block = %v", agent)
		}
		// The struct's Go field names never reach the output.
		for _, k := range []string{"ID", "Title", "Dir", "ClaimedBy"} {
			if _, ok := m[k]; ok {
				t.Fatalf("field %q encoded under its Go name: %v", k, m)
			}
		}
	}
	var gotOne map[string]any
	if err := json.Unmarshal(one.Bytes(), &gotOne); err != nil {
		t.Fatal(err)
	}
	checkWire(t, gotOne)

	var many bytes.Buffer
	if err := printJSON(&many, []kanban.Item{it, it}); err != nil {
		t.Fatal(err)
	}
	var gotMany []map[string]any
	if err := json.Unmarshal(many.Bytes(), &gotMany); err != nil {
		t.Fatal(err)
	}
	if len(gotMany) != 2 {
		t.Fatalf("a list of 2 items encoded %d", len(gotMany))
	}
	for _, m := range gotMany {
		checkWire(t, m)
	}

	// Anything else is encoded as it is.
	var other bytes.Buffer
	if err := printJSON(&other, map[string]int{"open": 3}); err != nil {
		t.Fatal(err)
	}
	if got, want := other.String(), "{\n  \"open\": 3\n}\n"; got != want {
		t.Fatalf("printJSON(map) = %q, want %q", got, want)
	}
}

func TestShortCommit(t *testing.T) {
	cases := map[string]string{
		"0123456789abcdef0123": "0123456789ab",
		"0123456789ab":         "0123456789ab",
		"abc":                  "abc",
		"":                     "",
	}
	for in, want := range cases {
		if got := shortCommit(in); got != want {
			t.Errorf("shortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActivatePluginsWiresEveryExtensionPoint(t *testing.T) {
	skills, hk, profiles, prompts := tools.ExtraSkillRoots, hooks.Extra, agentprofile.ExtraProfiles, promptlib.ExtraEntries
	t.Cleanup(func() {
		tools.ExtraSkillRoots, hooks.Extra, agentprofile.ExtraProfiles, promptlib.ExtraEntries = skills, hk, profiles, prompts
	})
	tools.ExtraSkillRoots, hooks.Extra, agentprofile.ExtraProfiles, promptlib.ExtraEntries = nil, nil, nil, nil

	activatePlugins()
	if tools.ExtraSkillRoots == nil || hooks.Extra == nil || agentprofile.ExtraProfiles == nil || promptlib.ExtraEntries == nil {
		t.Fatal("activatePlugins left an extension point unset")
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	for _, e := range promptlib.ExtraEntries() {
		if !e.Enabled {
			t.Errorf("plugin prompt %q is not enabled", e.Name)
		}
	}
}

func TestWorkerNotifierIsOffUnlessEnabledWithABackend(t *testing.T) {
	on, off := true, false
	for name, n := range map[string]*config.NotificationSettings{
		"no notification settings": nil,
		"enabled unset":            {Backend: notify.BackendBell},
		"disabled":                 {Enabled: &off, Backend: notify.BackendBell},
	} {
		if workerNotifier(config.Settings{Notifications: n}, "builder", false) != nil {
			t.Errorf("%s: got a notifier, want none", name)
		}
	}
	// A named backend is used as given, so this does not depend on the host.
	if workerNotifier(config.Settings{Notifications: &config.NotificationSettings{Enabled: &on, Backend: notify.BackendBell}}, "builder", false) == nil {
		t.Error("enabled with the bell backend: got no notifier")
	}
}

func TestRepoPolicyFollowsTheGuardrailsSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	t.Setenv("HOME", home)
	repo := t.TempDir()

	_, pol, err := repoPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(pol, posture.Defaults()) {
		t.Fatalf("with no settings the policy is %v, want the defaults %v", pol, posture.Defaults())
	}

	path, err := config.GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"guardrails": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, pol, err := repoPolicy(repo)
	if err != nil {
		t.Fatal(err)
	}
	if settings.GuardrailsEnabled() {
		t.Fatalf("guardrails still on after the user turned them off (settings at %s)", path)
	}
	if !maps.Equal(pol, posture.AllIgnore()) {
		t.Fatalf("with guardrails off the policy is %v, want every gate ignored", pol)
	}
}
