package rc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func noState() (config.State, error) { return config.State{}, nil }

func noEnv(string) string { return "" }

func TestBuildModelsDefaultFollowsSettingsThenState(t *testing.T) {
	state := func() (config.State, error) {
		return config.State{Provider: "cloudflare-ai-gateway", Model: "@cf/state"}, nil
	}
	m := buildModels(config.Settings{Model: "@cf/settings"}, state, noEnv, nil)
	if d := m.Default; d.Provider != "cloudflare-ai-gateway" || d.Model != "@cf/settings" || d.Routed {
		t.Fatalf("default = %+v", d)
	}
}

func TestBuildModelsFallsBackToTheResolveDefault(t *testing.T) {
	m := buildModels(config.Settings{}, noState, noEnv, nil)
	if m.Default.Provider != "openai" || m.Default.Model == "" {
		t.Fatalf("default = %+v, want openai with its default model", m.Default)
	}
	env := func(k string) string {
		if k == "BELAI_PROVIDER" {
			return "Anthropic"
		}
		return ""
	}
	if got := buildModels(config.Settings{}, noState, env, nil).Default.Provider; got != "anthropic" {
		t.Fatalf("BELAI_PROVIDER default = %q", got)
	}
}

func TestBuildModelsRoutedFollowsRoutingKind(t *testing.T) {
	s := config.Settings{Routing: &config.RoutingSettings{Kind: config.RoutingRouted}}
	if !buildModels(s, noState, noEnv, nil).Default.Routed {
		t.Fatal("routing.kind routed must advertise Routed")
	}
}

func TestBuildModelsListsOnlyConfiguredProvidersWithoutDuplicates(t *testing.T) {
	m := buildModels(config.Settings{Provider: "anthropic", Model: "my-private-model"}, noState, noEnv, []string{"openai", "anthropic"})
	if len(m.Providers) != 2 || m.Providers[0].Name != "anthropic" || m.Providers[1].Name != "openai" {
		t.Fatalf("providers = %+v", m.Providers)
	}
	seen := map[string]bool{}
	for _, id := range m.Providers[0].Models {
		if seen[id] {
			t.Fatalf("duplicate model %q", id)
		}
		seen[id] = true
	}
	if !seen["my-private-model"] || !seen["claude-opus-4-5"] {
		t.Fatalf("anthropic models = %v, want the selected and the catalogue default", m.Providers[0].Models)
	}
	if len(buildModels(config.Settings{}, noState, noEnv, nil).Providers) != 0 {
		t.Fatal("no credentials must list no providers")
	}
}

func TestCheckOverride(t *testing.T) {
	models := func() *sessionsync.RCModels {
		return &sessionsync.RCModels{Providers: []sessionsync.RCProvider{{Name: "anthropic"}}}
	}
	for _, c := range []struct {
		name, prov, model, effort string
		bad                       bool
	}{
		{name: "nothing named", prov: "", model: "", effort: ""},
		{name: "configured provider", prov: "anthropic", model: "claude-opus-4-5", effort: "high"},
		{name: "model alone", prov: "", model: "@cf/moonshotai/kimi-k2.6"},
		{name: "unconfigured provider", prov: "openai", bad: true},
		{name: "flag as provider", prov: "-x", bad: true},
		{name: "space in model", prov: "anthropic", model: "a b", bad: true},
		{name: "shell in effort", prov: "anthropic", effort: "high;rm", bad: true},
	} {
		got := checkOverride(c.prov, c.model, c.effort, models)
		if (got != "") != c.bad {
			t.Errorf("%s: checkOverride = %q, bad=%v", c.name, got, c.bad)
		}
	}
	if checkOverride("anthropic", "", "", func() *sessionsync.RCModels { return nil }) == "" {
		t.Error("a host that cannot list its providers must refuse a named provider")
	}
	if !strings.Contains(checkOverride("openai", "", "", models), "openai") {
		t.Error("the refusal names the provider")
	}
}

func TestStartChildPassesTheModelChoice(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	exe := filepath.Join(dir, "fake-belai")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argv + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(c Child) string {
		c.Exe, c.Cwd, c.Dispatch, c.SessionID, c.Idle, c.Prompt = exe, dir, "d1", "s1", time.Minute, "hi"
		_, wait, err := startChild(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := wait(); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(argv)
		return string(b)
	}
	if got := run(Child{}); strings.Contains(got, "-provider") || strings.Contains(got, "-model") || strings.Contains(got, "-effort") {
		t.Errorf("no choice must pass no flags, got %q", got)
	}
	got := run(Child{Provider: "anthropic", Model: "claude-opus-4-5", Effort: "high"})
	for _, want := range []string{"-provider\nanthropic", "-model\nclaude-opus-4-5", "-effort\nhigh"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv %q lacks %q", got, want)
		}
	}
}

func TestStartChildPassesTheGitSyncSwitch(t *testing.T) {
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	exe := filepath.Join(dir, "fake-belai")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(sw *bool) string {
		_, wait, err := startChild(Child{Exe: exe, Cwd: dir, Dispatch: "d1", SessionID: "s1", Idle: time.Minute, Prompt: "hi", GitSync: sw})
		if err != nil {
			t.Fatal(err)
		}
		_ = wait()
		b, _ := os.ReadFile(argv)
		return string(b)
	}
	on, off := true, false
	if got := run(nil); strings.Contains(got, "-git-sync") {
		t.Errorf("no switch must pass no flag, got %q", got)
	}
	if got := run(&off); !strings.Contains(got, "-git-sync\noff") {
		t.Errorf("off: %q", got)
	}
	if got := run(&on); !strings.Contains(got, "-git-sync\non") {
		t.Errorf("on: %q", got)
	}
}
