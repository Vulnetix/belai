package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/vulnetixcli"
	"github.com/vulnetix/belai/internal/vulnetixenroll"
)

// isolate points Belai's state and the Vulnetix CLI's home at temp dirs.
func isolate(t *testing.T) (home, workdir string) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	home = t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"VULNETIX_API_KEY", "VULNETIX_ORG_ID", "VULNETIX_API_TOKEN", "VVD_ORG", "VVD_SECRET", "VULNETIX_CREDENTIALS_DIR"} {
		t.Setenv(k, "")
	}
	return home, t.TempDir()
}

func TestGettingStartedOnlyOnFirstInteractiveLaunch(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.maybeStartGettingStarted(Options{})
	a.Init()
	if a.view != viewGettingStarted {
		t.Fatalf("view = %v, want getting started on first launch", a.view)
	}

	for name, opts := range map[string]Options{"seed prompt": {Prompt: "hi"}, "resume": {ResumeSession: "s"}} {
		b := New(Options{Workdir: wd})
		b.maybeStartGettingStarted(opts)
		b.Init()
		if b.view == viewGettingStarted {
			t.Errorf("%s: opened getting started", name)
		}
	}

	c := New(Options{Workdir: wd})
	c.state.OnboardedAt = "2026-01-01T00:00:00Z"
	c.maybeStartGettingStarted(Options{})
	c.Init()
	if c.view == viewGettingStarted {
		t.Fatal("opened getting started for an onboarded user")
	}
}

// gsPlain strips the styling from a rendered page.
func gsPlain(s string) string { return ansi.Strip(s) }

func openWelcome(t *testing.T, width int) *App {
	t.Helper()
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.Update(tea.WindowSizeMsg{Width: width, Height: 50})
	a.openGettingStarted()
	return a
}

func TestGettingStartedWelcomeTeachesThreeIdeas(t *testing.T) {
	a := openWelcome(t, 110)
	page := gsPlain(a.gettingStartedView())
	for i, sec := range gsSections {
		if !strings.Contains(page, fmt.Sprintf("%d  %s", i+1, sec.title)) {
			t.Errorf("welcome lacks section %d %q", i+1, sec.title)
		}
	}
	if len(gsSections) != 3 {
		t.Fatalf("%d sections, want 3", len(gsSections))
	}
	for _, want := range []string{"shift+tab", "f3", "f4", "/permissions", "/model", "/providers", "/agent", "/kanban", "/fleet", "/firewall", "/mcp", "/resume", "/help"} {
		if !strings.Contains(page, want) {
			t.Errorf("welcome never names %s", want)
		}
	}
	for _, r := range page {
		if r == '—' || r == '–' {
			t.Fatalf("welcome prose carries a dash glyph: %q", page)
		}
	}
	if gsPage(gsWelcome) != 1 || gsPage(gsEnable) != gsPageCount {
		t.Fatalf("page numbering: welcome %d, enable %d of %d", gsPage(gsWelcome), gsPage(gsEnable), gsPageCount)
	}
	// There is no second screen of shortcut keys any more: enter goes to the CLI step.
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.gsState.step != gsCLI {
		t.Fatalf("enter on welcome went to step %v, want the CLI step", a.gsState.step)
	}
}

func TestGettingStartedHeaderShowsEscHintAndCount(t *testing.T) {
	a := openWelcome(t, 110)
	head := strings.SplitN(gsPlain(a.gettingStartedView()), "\n", 3)
	var line string
	for _, h := range head {
		if strings.Contains(h, "Getting started") {
			line = h
		}
	}
	for _, want := range []string{"Getting started · Welcome 1/4", "Press esc to start using Belai (bell-lay) now"} {
		if !strings.Contains(line, want) {
			t.Errorf("header %q lacks %q", line, want)
		}
	}
	if strings.Index(line, "Press esc") < strings.Index(line, "Welcome") {
		t.Errorf("the hint should sit to the right of the title: %q", line)
	}
}

func TestGettingStartedHeaderHintShrinksOnANarrowTerminal(t *testing.T) {
	a := openWelcome(t, 56)
	page := gsPlain(a.gettingStartedView())
	if strings.Contains(page, "(bell-lay)") {
		t.Fatalf("the pronunciation should drop first on a narrow terminal: %q", page)
	}
	if !strings.Contains(page, "esc") {
		t.Fatalf("some esc hint must remain: %q", page)
	}
}

func TestGettingStartedEscHintMatchesWhatEscDoes(t *testing.T) {
	a := openWelcome(t, 110)
	a.gsGo(gsSignup)
	if page := gsPlain(a.gettingStartedView()); !strings.Contains(page, "esc to go back") || strings.Contains(page, "start using Belai") {
		t.Fatalf("sign-up must not promise to start Belai: %q", page)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewGettingStarted || a.gsState.step != gsAccount {
		t.Fatalf("esc on sign-up should go back to the account step, got view %v step %v", a.view, a.gsState.step)
	}
	// An info page finishes.
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("esc on the account step should start Belai, view = %v", a.view)
	}
}

func TestGettingStartedEscOnCLIStartsBelaiUnlessInstalling(t *testing.T) {
	a := openWelcome(t, 110)
	a.gsGo(gsCLI)
	a.gsState.installing = true
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewGettingStarted {
		t.Fatal("esc must not walk away from an install in progress")
	}
	a.gsState.installing = false
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("esc on the CLI step should start Belai, view = %v", a.view)
	}
	st, err := config.LoadState()
	if err != nil || st.OnboardedAt == "" {
		t.Fatalf("finishing from the CLI step must be remembered: %+v %v", st, err)
	}
}

func TestGettingStartedNextOnTheCLIStepSaysItChecksTheNetwork(t *testing.T) {
	a := openWelcome(t, 140)
	a.gsGo(gsCLI)
	a.Update(gsCLIProbeMsg{path: "/usr/local/bin/vulnetix"})
	page := gsPlain(a.gettingStartedView())
	for _, want := range []string{"AI Firewall", "Vulnetix MCP server", "over the network", "next (network check)"} {
		if !strings.Contains(page, want) {
			t.Errorf("CLI step lacks %q: %q", want, page)
		}
	}
	// With the install choice on offer the hint is still there.
	b := openWelcome(t, 140)
	b.gsGo(gsCLI)
	plan, _ := vulnetixcli.PlanInstall("linux", func(string) (string, error) { return "/bin/brew", nil })
	b.Update(gsCLIProbeMsg{plan: plan, planOK: true})
	if page := gsPlain(b.gettingStartedView()); !strings.Contains(page, "network check") {
		t.Errorf("install choice lacks the network note: %q", page)
	}
}

func TestGettingStartedWelcomeShowsMacHintsWhenOn(t *testing.T) {
	macHints(t, true)
	a := openWelcome(t, 140)
	macHints(t, true) // New resets the switch from the platform
	if page := gsPlain(a.gettingStartedView()); !strings.Contains(page, "f3 (Fn+Mission Control)") || !strings.Contains(page, "f4 (Fn+Launchpad)") {
		t.Fatalf("welcome lacks the Mac form: %q", page)
	}
	macHints(t, false)
	if page := gsPlain(a.gettingStartedView()); strings.Contains(page, "Fn+") {
		t.Fatalf("Mac form shown with the setting off: %q", page)
	}
}

func TestGettingStartedEscSkipsAndIsRemembered(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.openGettingStarted()
	a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("view = %v after esc", a.view)
	}
	st, err := config.LoadState()
	if err != nil || st.OnboardedAt == "" {
		t.Fatalf("onboarding not recorded: %+v %v", st, err)
	}
}

func TestGettingStartedInstallIsAnExplicitChoice(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.openGettingStarted()
	a.gsGo(gsCLI)
	plan, _ := vulnetixcli.PlanInstall("linux", func(string) (string, error) { return "/bin/brew", nil })
	a.Update(gsCLIProbeMsg{plan: plan, planOK: true})
	if !strings.Contains(a.gettingStartedView(), "brew install vulnetix/tap/vulnetix") {
		t.Fatal("the install command is not shown before it runs")
	}
	// Enter on the default choice skips: nothing is installed.
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.gsState.installing || a.gsState.step != gsAccount {
		t.Fatalf("default choice installed or did not move on: step %v installing %v", a.gsState.step, a.gsState.installing)
	}
	_ = cmd
}

func TestGettingStartedPasswordNeverLingers(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	a.openGettingStarted()
	a.gsGo(gsSignup)
	const secret = "hunter2-very-secret"
	for i := range a.gsState.fields {
		switch a.gsState.fields[i].key {
		case "email":
			a.gsState.fields[i].value = "you@example.com"
		case "password", "password_repeat":
			a.gsState.fields[i].value = secret
		}
	}
	if strings.Contains(a.gettingStartedView(), secret) {
		t.Fatal("the password is rendered")
	}
	// The returned command (the network call) is not run; only the form
	// handling matters here.
	a.gsState.enroll = vulnetixenroll.New()
	_ = a.gsSubmitSignup()
	for _, f := range a.gsState.fields {
		if f.masked && f.value != "" {
			t.Fatalf("%s kept after submit", f.key)
		}
	}
	for _, m := range a.messages {
		if strings.Contains(m.Content, secret) {
			t.Fatal("the password reached the transcript")
		}
	}
	// Leaving the form clears it too.
	a.gsState.fields[2].value = secret
	a.gsGo(gsAccount)
	if a.gsState.fields[2].value != "" {
		t.Fatal("password kept after leaving the form")
	}
}

func writeVulnetixCred(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".vulnetix")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"org_id":"3674ddf9-67cc-4a2d-9b16-a591f6d4412d","api_key":"cafef00d-secret","method":"apikey"}`
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVulnetixMCPInstallWritesTheReferenceNotTheSecret(t *testing.T) {
	home, wd := isolate(t)
	prev := mcp.Active()
	mcp.SetActive(nil)
	defer mcp.SetActive(prev)

	if _, err := installVulnetixMCP(context.Background(), wd); err == nil || !strings.Contains(err.Error(), "/vulnetix setup") {
		t.Fatalf("install without a credential: %v", err)
	}

	writeVulnetixCred(t, home)
	if _, err := installVulnetixMCP(context.Background(), wd); err != nil {
		t.Fatal(err)
	}
	path, _ := config.GlobalSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cafef00d") {
		t.Fatal("the CLI secret was written to settings.json")
	}
	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	got := s.MCP.Servers[config.VulnetixMCPName]
	if got.URL != config.VulnetixMCPURL || got.Headers["Authorization"] != config.VulnetixCLIRef || got.Transport != "http" {
		t.Fatalf("entry = %+v", got)
	}

	if err := removeVulnetixMCP(wd); err != nil {
		t.Fatal(err)
	}
	s, _ = config.LoadGlobal()
	if s.MCP != nil && s.MCP.Servers[config.VulnetixMCPName].URL != "" {
		t.Fatal("remove left the entry")
	}
}

func TestFirewallKeySyncOnlyWhileFirewallOn(t *testing.T) {
	_, wd := isolate(t)
	a := New(Options{Workdir: wd})
	if cmd := a.syncFirewallKey("openai"); cmd != nil {
		t.Fatal("synced a key with the firewall off")
	}
}

// TestFirewallKeySyncSendsTheKeyOnStdin runs the sync against a fake vulnetix
// that records its argv, environment and stdin.
func TestFirewallKeySyncSendsTheKeyOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	home, wd := isolate(t)
	writeVulnetixCred(t, home)
	const key = "sk-test-provider-key-123"
	t.Setenv("OPENAI_API_KEY", key)
	bin := t.TempDir()
	out := filepath.Join(bin, "out")
	script := "#!/bin/sh\necho \"ARGV $*\" > " + out + "\nenv >> " + out + "\necho STDIN >> " + out + "\ncat >> " + out + "\n"
	if err := os.WriteFile(filepath.Join(bin, "vulnetix"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	r, err := credentials.NewResolver(wd)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: wd, Resolver: r})
	on := true
	a.firewallOverride = &on
	a.resolver.SetFirewallEnabled(&on)
	cmd := a.syncFirewallKey("openai")
	if cmd == nil {
		t.Fatal("no sync with the firewall on")
	}
	msg, ok := cmd().(firewallSyncMsg)
	if !ok || msg.err != nil {
		t.Fatalf("msg = %+v", msg)
	}
	data, _ := os.ReadFile(out)
	argv, rest, _ := strings.Cut(string(data), "\n")
	env, stdin, _ := strings.Cut(rest, "STDIN\n")
	if strings.Contains(argv, key) || !strings.Contains(argv, "ai-firewall key set openai --stdin") {
		t.Fatalf("argv = %q", argv)
	}
	if strings.Contains(env, key) {
		t.Fatal("the provider key reached the CLI's environment")
	}
	if strings.TrimSpace(stdin) != key {
		t.Fatalf("stdin = %q", stdin)
	}
}

func TestMCPCleanDropsHTMLFromAServerError(t *testing.T) {
	err := `mcp server returned HTTP 403 (server said: HTTP 403: <html><head><title>403 Forbidden</title><link rel="stylesheet" href="/a.css"><link rel="ic`
	got := mcpClean(err, 200)
	if strings.ContainsAny(got, "<>") || strings.Contains(got, "href") {
		t.Fatalf("markup left in %q", got)
	}
	if !strings.Contains(got, "HTTP 403") || !strings.Contains(got, "403 Forbidden") {
		t.Fatalf("the status and page title should survive: %q", got)
	}
}
