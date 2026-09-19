package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentpool"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/promptlib"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "signet-tui-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	// Isolate state/session files and provider env from the developer machine.
	os.Setenv("SIGNET_HOME", tmp)
	os.Setenv("OPENAI_API_KEY", "")
	os.Setenv("ANTHROPIC_API_KEY", "")
	os.Setenv("CLOUDFLARE_API_KEY", "")
	os.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	os.Setenv("CLOUDFLARE_GATEWAY_ID", "")
	os.Setenv("CF_AIG_TOKEN", "")
	os.Setenv("CF_AIG_URL", "")
	os.Setenv("CF_ACCOUNT_ID", "")
	os.Setenv("SIGNET_PROVIDER", "")
	os.Setenv("PI_PROVIDER", "")
	os.Setenv("SIGNET_MODEL", "")
	os.Setenv("SIGNET_EFFORT", "")
	os.Exit(m.Run())
}

// fakeClassifier records payloads and returns a fixed result (or error).
type fakeClassifier struct {
	raw      string
	err      error
	payloads []rolemanager.ClassifierPayload
}

func (f *fakeClassifier) Classify(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
	f.payloads = append(f.payloads, p)
	if f.err != nil {
		return "", f.err
	}
	return f.raw, nil
}

func TestNewAppView(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	if a == nil {
		t.Fatalf("NewApp returned nil")
	}
	if v := a.View(); v == "" {
		t.Fatalf("View returned empty string")
	}
}

func TestToggleCavemanShortcut(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	if a.settings.CavemanEnabled() {
		t.Fatal("caveman should default to off")
	}

	// Simulate a cached session: toggling caveman must drop it so the next
	// turn picks up the new system prompt.
	a.agent = &agent.Session{}

	cmd := a.toggleCaveman()
	if cmd != nil {
		t.Fatalf("toggleCaveman returned a command: %v", cmd)
	}
	if !a.settings.CavemanEnabled() {
		t.Fatal("caveman should be enabled after toggle")
	}
	if a.agent != nil {
		t.Fatal("caveman toggle should invalidate the cached agent session")
	}
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "caveman: on") {
		t.Fatalf("expected system message caveman: on, got %q", last.Text())
	}

	cmd = a.toggleCaveman()
	if cmd != nil {
		t.Fatalf("toggleCaveman returned a command: %v", cmd)
	}
	if a.settings.CavemanEnabled() {
		t.Fatal("caveman should be disabled after second toggle")
	}
	last = a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "caveman: off") {
		t.Fatalf("expected system message caveman: off, got %q", last.Text())
	}
}

func TestToggleCavemanPersistsToProjectSettings(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})

	// Toggle on writes to the default (project) scope.
	_ = a.toggleCaveman()
	proj, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Caveman == nil || !*proj.Caveman {
		t.Fatalf("project settings must have caveman=true after toggle, got %+v", proj.Caveman)
	}

	// Toggle off writes false so a global true is explicitly overridden.
	_ = a.toggleCaveman()
	proj, err = config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Caveman == nil || *proj.Caveman {
		t.Fatalf("project settings must have caveman=false after second toggle, got %+v", proj.Caveman)
	}
}

func TestToggleCavemanOverridesGlobalDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workdir := t.TempDir()

	on := true
	_ = config.SaveGlobal(config.Settings{Caveman: &on})

	a := New(Options{Workdir: workdir})
	if !a.settings.CavemanEnabled() {
		t.Fatal("effective settings should inherit global caveman=true")
	}

	_ = a.toggleCaveman() // writes false to project
	if a.settings.CavemanEnabled() {
		t.Fatal("caveman should be disabled after project-level toggle")
	}

	proj, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Caveman == nil || *proj.Caveman {
		t.Fatalf("project settings must explicitly set caveman=false to shadow global true")
	}
}

func TestClassifyModeSelectsPlan(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "PLAN"})

	a.classifyMode("figure out how to refactor this")

	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if len(a.messages) == 0 {
		t.Fatalf("expected a mode message")
	}
}

// A decision that lands on the mode already selected tells the user nothing
// the footer chip is not showing, so it writes no transcript line.
func TestClassifyModeSameModeIsSilent(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "AGENT"})
	a.mode = "agent"
	before := len(a.messages)

	a.classifyMode("do the thing")

	if a.mode != "agent" {
		t.Fatalf("mode = %q, want agent", a.mode)
	}
	if len(a.messages) != before {
		t.Fatalf("expected no message for an unchanged mode, got %q", a.messages[len(a.messages)-1].Text())
	}
}

// An unchanged mode that still launches explore agents keeps that line: it
// describes the turn, not the mode chip.
func TestClassifyModeSameModeStillReportsExplore(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "PLAN"})
	a.mode = "plan"

	a.classifyMode("figure out how to refactor this")

	last := a.messages[len(a.messages)-1].Text()
	if last != "launch explore agents" {
		t.Fatalf("last message = %q, want \"launch explore agents\"", last)
	}
}

// A mode that did change is still announced.
func TestClassifyModeChangeIsAnnounced(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "PLAN"})
	a.mode = "agent"

	a.classifyMode("figure out how to refactor this")

	last := a.messages[len(a.messages)-1].Text()
	if last != "mode: plan (launch explore agents)" {
		t.Fatalf("last message = %q, want \"mode: plan (launch explore agents)\"", last)
	}
}

func TestClassifyModeGoalOverLimitDefaultsToAgent(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "GOAL"})

	long := make([]byte, rolemanager.DefaultGoalPromptLengthLimit+1)
	for i := range long {
		long[i] = 'x'
	}
	a.classifyMode(string(long))

	if a.mode != "agent" {
		t.Fatalf("mode = %q, want agent (goal length exceeded)", a.mode)
	}
	if a.modeWarning == "" {
		t.Fatalf("expected a length warning")
	}
}

func TestClassifyModeNamedAgent(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(&fakeClassifier{raw: "AGENT"})

	a.classifyMode("review this @agent:security-expert")

	if a.mode != "agent" {
		t.Fatalf("mode = %q, want agent", a.mode)
	}
	if a.namedAgent != "security-expert" {
		t.Fatalf("namedAgent = %q, want security-expert", a.namedAgent)
	}
}

func TestClassifyModeSkippedWithoutClassifier(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(nil)
	a.mode = "agent"
	before := len(a.messages)
	a.classifyMode("anything")
	if a.mode != "agent" {
		t.Fatalf("mode = %q, want unchanged agent", a.mode)
	}
	if len(a.messages) != before {
		t.Fatalf("no classifier installed, expected no new messages, got %d (was %d)", len(a.messages), before)
	}
}

func TestStreamChunksAppendToAssistantMessage(t *testing.T) {
	a := New(Options{})
	a.messages = append(a.messages, components.Message{Role: "user", Content: "hi"})
	a.messages = append(a.messages, components.Message{Role: "assistant"})

	m, _ := a.Update(streamChunkMsg{Text: "hello"})
	a = m.(*App)
	if a.messages[len(a.messages)-1].Text() != "hello" {
		t.Fatalf("content = %q", a.messages[len(a.messages)-1].Text())
	}

	m, _ = a.Update(streamChunkMsg{Text: " world"})
	a = m.(*App)
	if a.messages[len(a.messages)-1].Text() != "hello world" {
		t.Fatalf("content = %q", a.messages[len(a.messages)-1].Text())
	}
}

func TestNoCredentialsStillRenders(t *testing.T) {
	a := New(Options{})
	if v := a.View(); v == "" {
		t.Fatalf("View returned empty")
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Content, "/credentials") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected /credentials hint in system messages, got %v", a.messages)
	}
}

func TestProviderErrorBecomesSystemMessage(t *testing.T) {
	a := New(Options{})
	_, _ = a.Update(streamChunkMsg{Err: fmt.Errorf("boom"), Done: true})
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Content, "boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected error as system message")
	}
}

func TestMaskedEditorHidesValue(t *testing.T) {
	e := components.NewEditor()
	e.Masked = true
	e.SetValue("secret")
	if !strings.Contains(e.View(), "•") {
		t.Fatalf("expected masked dots")
	}
	if e.Value() != "secret" {
		t.Fatalf("Value should remain truthful")
	}
}

func TestSetCredentialClearsEditor(t *testing.T) {
	a := New(Options{})
	a.view = viewCredentials
	a.credentialState.setMode = true
	a.editor.Masked = true
	a.editor.SetValue("secret")

	m, _ := a.handleCredentialKey(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if a.credentialState.setMode {
		t.Fatalf("expected setMode to be false")
	}
	if a.editor.Masked {
		t.Fatalf("expected editor unmasked")
	}
	if a.editor.Value() != "" {
		t.Fatalf("expected editor cleared")
	}
}

func TestEnterWithoutCredentialsDoesNotCallProvider(t *testing.T) {
	called := false
	transport := &fatalTransport{t: t, called: &called}
	client := &http.Client{Transport: transport}
	a := New(Options{Client: client})

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	cmd := a.send(a.buildTurns())
	msg := cmd()
	ev := msg.(agentEventMsg)
	if ev.Kind != agent.EventErrorKind || ev.Err == nil {
		t.Fatalf("expected error event, got %+v", ev)
	}
	if *transport.called {
		t.Fatalf("provider should not have been called")
	}
}

type fatalTransport struct {
	t      *testing.T
	called *bool
}

func (f *fatalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*f.called = true
	f.t.Fatalf("RoundTrip called unexpectedly")
	return nil, nil
}

func TestSendCallsProvider(t *testing.T) {
	srv := newAgentSSEServer(t, "pong")
	defer srv.Close()

	src := &fakeCredentialSource{vals: map[string]string{"openai:api_key": "sk-test"}}
	cfg, status := run.Prepare("gpt-5", "openai", src)
	if !status.Configured {
		t.Fatalf("expected configured")
	}
	cfg.BaseURL = srv.URL
	a := New(Options{Client: srv.Client(), Provider: "openai", Model: "gpt-5"})
	a.cfg = cfg
	a.status = status

	a = drainAgent(t, a, a.send([]run.Turn{{Role: "user", Content: "ping"}}))
	if len(a.messages) == 0 || a.messages[len(a.messages)-1].Content != "pong" {
		t.Fatalf("expected assistant reply 'pong', got %v", a.messages)
	}
}

func newAgentSSEServer(t *testing.T, finalReply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var system string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		reply := finalReply
		if strings.Contains(system, "security classifier") {
			reply = "SAFE"
		} else if strings.Contains(system, "operating-mode classifier") {
			reply = "AGENT"
		} else if strings.Contains(system, "goal-progress evaluator") {
			reply = "GOAL_COMPLETE"
		} else if strings.Contains(system, "plan-progress evaluator") {
			reply = "PLAN_COMPLETE"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		b, _ := json.Marshal(map[string]any{
			"id":     "x",
			"object": "chat.completion",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
}

func drainAgent(t *testing.T, a *App, cmd tea.Cmd) *App {
	t.Helper()
	for steps := 0; cmd != nil; steps++ {
		if steps > 10000 {
			t.Fatalf("drainAgent did not terminate")
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			var cmds []tea.Cmd
			for _, c := range batch {
				m := c()
				if nested, ok := m.(tea.BatchMsg); ok {
					// A command returned another batch (e.g. startAgent inside a
					// cold-send batch); flatten it so no command is dropped.
					cmds = append(cmds, nested...)
					continue
				}
				mm, next := a.Update(m)
				a = mm.(*App)
				cmds = append(cmds, next)
			}
			cmd = tea.Batch(cmds...)
			continue
		}
		m, next := a.Update(msg)
		a = m.(*App)
		cmd = next
	}
	return a
}

type fakeCredentialSource struct {
	vals map[string]string
}

func (f *fakeCredentialSource) Lookup(provider, field string) (value, origin string, ok bool) {
	key := provider + ":" + field
	if v, ok := f.vals[key]; ok {
		return v, "fake", true
	}
	return "", "", false
}

func TestCredentialViewShowsProvenanceNotSecret(t *testing.T) {
	a := New(Options{})
	a.view = viewCredentials
	a.credentialState.sets = map[string]credentials.Set{
		"openai": {
			Provider: "openai",
			Values: map[string]credentials.Value{
				"api_key": {Field: "api_key", Location: "keychain", Source: credentials.SourceKeychain, Secret: true},
			},
		},
	}
	view := a.credentialView()
	if !strings.Contains(view, "keychain") {
		t.Fatalf("view should contain 'keychain'")
	}
	if strings.Contains(view, "sk-secret") {
		t.Fatalf("view should not contain the secret")
	}
}

func TestAssistantTurnsAccumulate(t *testing.T) {
	srv := newAgentSSEServer(t, "pong")
	defer srv.Close()

	src := &fakeCredentialSource{vals: map[string]string{"openai:api_key": "sk-test"}}
	cfg, status := run.Prepare("gpt-5", "openai", src)
	if !status.Configured {
		t.Fatalf("expected configured")
	}
	cfg.BaseURL = srv.URL
	a := New(Options{Client: srv.Client(), Provider: "openai", Model: "gpt-5"})
	a.cfg = cfg
	a.status = status

	a.messages = append(a.messages, components.Message{Role: "user", Content: "ping"})
	a = drainAgent(t, a, a.send(a.buildTurns()))

	a.messages = append(a.messages, components.Message{Role: "user", Content: "ping again"})
	turns := a.buildTurns()
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns (2 user + 1 assistant), got %d", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Content != "ping" {
		t.Fatalf("turn 0 wrong: %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Content != "pong" {
		t.Fatalf("turn 1 wrong: %+v", turns[1])
	}
	if turns[2].Role != "user" || turns[2].Content != "ping again" {
		t.Fatalf("turn 2 wrong: %+v", turns[2])
	}
}

// ---------------------------------------------------------------------------
// Plan 1 regression tests
// ---------------------------------------------------------------------------

func TestCtrlDQuits(t *testing.T) {
	a := New(Options{})
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("ctrl+d must quit")
	}
	if msg, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+d cmd = %#v, want quit", msg)
	}
}

func TestCtrlCCopiesPromptDoesNotQuit(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("hello")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c with text should copy")
	}
	msg := cmd()
	if _, ok := msg.(copiedMsg); !ok {
		t.Fatalf("ctrl+c cmd produced %#v, want copiedMsg", msg)
	}
	if a.view != viewChat {
		t.Fatalf("ctrl+c must not quit")
	}
}

func TestCredentialSetRebuildsClassifier(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	a := New(Options{})
	a.SetClassifier(nil)
	a.cfg = run.Config{Provider: "openai", Model: "gpt-5", BaseURL: "https://api.openai.com/v1"}
	a.status = run.Status{}
	a.resolver = nil
	a.refreshProvider()
	if a.classifier == nil {
		t.Fatalf("expected classifier rebuilt after credential set")
	}
	if !a.status.Configured {
		t.Fatalf("expected configured after credential set")
	}
}

func TestCredentialClearRefreshesProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	a := New(Options{})
	a.SetClassifier(nil)
	a.cfg = run.Config{Provider: "openai", Model: "gpt-5"}
	a.status = run.Status{Configured: true}
	a.resolver = nil
	a.refreshProvider()
	if a.status.Configured {
		t.Fatalf("expected unconfigured after clear")
	}
	if a.classifier != nil {
		t.Fatalf("expected nil classifier after clear")
	}
}

func TestSettingsViewRendersAndEscapes(t *testing.T) {
	a := New(Options{})
	a.push(viewSettings)
	if v := a.View(); !strings.Contains(v, "Settings") {
		t.Fatalf("settings view should render, got %q", v)
	}
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)
	if a.view != viewChat {
		t.Fatalf("esc should return to chat, got view %v", a.view)
	}
}

func TestPermissionsEscReturnsToSettings(t *testing.T) {
	a := New(Options{})
	a.push(viewSettings)
	a.push(viewPermissions)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)
	if a.view != viewSettings {
		t.Fatalf("permissions esc should land on settings, got view %v", a.view)
	}
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)
	if a.view != viewChat {
		t.Fatalf("settings esc should land on chat, got view %v", a.view)
	}
}

// ---------------------------------------------------------------------------
// Plan 2 tests
// ---------------------------------------------------------------------------

const validSummary = "## Goal\nfinish the job\n## Constraints & Preferences\nnone\n## Progress\n### Done\nx\n### In Progress\ny\n### Blocked\nz\n## Key Decisions\na\n## Next Steps\nb\n## Critical Context\nc"

func TestClearStartsNewSession(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	old := a.sessionID
	a.messages = append(a.messages, components.Message{Role: "user", Content: "hi"})
	a.sessionName = "old name"
	a.summary = "old summary"

	a.handleCommand("/clear")

	if a.sessionID == old {
		t.Fatalf("/clear should change session id")
	}
	if len(a.messages) != 0 {
		t.Fatalf("/clear should empty messages")
	}
	if a.sessionName != "" {
		t.Fatalf("/clear should clear name")
	}
	if a.summary != "" {
		t.Fatalf("/clear should clear summary")
	}
}

func TestNewAliasMatchesClear(t *testing.T) {
	a := New(Options{})
	a.sessionName = "x"
	a.summary = "y"
	a.handleCommand("/new")
	if a.sessionName != "" || a.summary != "" {
		t.Fatalf("/new should behave like /clear")
	}
}

func TestRenameWritesSessionNameEntry(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.handleCommand("/rename my demo session")
	if a.sessionName != "my demo session" {
		t.Fatalf("sessionName = %q", a.sessionName)
	}
	st, _ := session.NewStore()
	entries, err := st.Read(workdir, a.sessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == session.EntryTypeSessionName && e.Content == "my demo session" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a session_name entry, got %+v", entries)
	}
}

func TestCompactHappyPath(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	fc := &fakeClassifier{raw: validSummary}
	a.SetClassifier(fc)
	a.sessionID = session.MustID()
	old := a.sessionID
	a.sessionName = "old name"
	a.messages = append(a.messages,
		components.Message{Role: "user", Content: "hi"},
		components.Message{Role: "assistant", Content: "hello"},
	)
	// Persist the old session so it exists on disk before compaction.
	a.appendEntry(session.Entry{Type: "user", Role: "user", Content: "hi"})
	a.appendEntry(session.Entry{Type: "assistant", Role: "assistant", Content: "hello"})

	cmd := a.handleCommand("/compact")
	if cmd == nil {
		t.Fatalf("compact command should return a Cmd")
	}
	msg := cmd()
	done, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("compact cmd produced %#v", msg)
	}
	if done.err != nil {
		t.Fatalf("compact err = %v", done.err)
	}
	a.handleCompactDone(done)

	if a.sessionID == old {
		t.Fatalf("compact should mint a new session id")
	}
	if a.sessionName != "old name" {
		t.Fatalf("name should carry over, got %q", a.sessionName)
	}
	if !a.usageStale {
		t.Fatalf("compacted session should mark usage stale")
	}
	if a.summary == "" {
		t.Fatalf("summary should be set")
	}

	// The new session's root entry links the parent.
	st, _ := session.NewStore()
	entries, err := st.Read(workdir, a.sessionID)
	if err != nil {
		t.Fatalf("read compacted session: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected root entry")
	}
	if parent, _ := entries[0].Meta["parent_session"].(string); parent != old {
		t.Fatalf("root entry parent_session = %v, want %s", entries[0].Meta["parent_session"], old)
	}

	// The old file is untouched.
	oldEntries, err := st.Read(workdir, old)
	if err != nil {
		t.Fatalf("old session should still be readable: %v", err)
	}
	if len(oldEntries) == 0 {
		t.Fatalf("old session should still have entries")
	}
}

func TestCompactFailureLeavesSessionIntact(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.SetClassifier(&fakeClassifier{err: errors.New("boom")})
	a.sessionID = session.MustID()
	old := a.sessionID
	a.messages = append(a.messages, components.Message{Role: "user", Content: "hi"})

	cmd := a.handleCommand("/compact")
	done := cmd().(compactDoneMsg)
	if done.err == nil {
		t.Fatalf("expected classifier error")
	}
	a.handleCompactDone(done)
	if a.sessionID != old {
		t.Fatalf("failed compact must not change session id")
	}
	if a.summary != "" {
		t.Fatalf("failed compact must not set summary")
	}
}

func TestCompactMalformedSummaryFails(t *testing.T) {
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{raw: "not a structured summary"})
	a.sessionID = session.MustID()
	old := a.sessionID
	a.messages = append(a.messages, components.Message{Role: "user", Content: "hi"})

	cmd := a.handleCommand("/compact")
	done := cmd().(compactDoneMsg)
	if done.err == nil {
		t.Fatalf("expected validation error")
	}
	a.handleCompactDone(done)
	if a.sessionID != old {
		t.Fatalf("malformed summary must not change session id")
	}
}

func TestBuildTurnsOnCompactedApp(t *testing.T) {
	a := New(Options{})
	a.summary = "the summary"
	a.messages = append(a.messages, components.Message{Role: "user", Content: "next"})
	turns := a.buildTurns()
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" || !strings.Contains(turns[0].Content, "the summary") {
		t.Fatalf("turn 0 should be the summary user turn: %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Content != rolemanager.SummaryAck {
		t.Fatalf("turn 1 should be the ack: %+v", turns[1])
	}
	if turns[2].Role != "user" || turns[2].Content != "next" {
		t.Fatalf("turn 2 wrong: %+v", turns[2])
	}
}

func TestAutoNamingMarksRequested(t *testing.T) {
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{raw: "My Session"})
	a.modeExplicit = true
	a.namedAgent = "signet:debug" // agent mode requires an engaged agent
	a.editor.SetValue("hello world")
	m, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if !a.nameRequested {
		t.Fatalf("first user message should mark nameRequested")
	}
	if cmd == nil {
		t.Fatalf("expected a command batch")
	}
}

func TestStreamDoneUsageClearsStale(t *testing.T) {
	a := New(Options{})
	a.usageStale = true
	a.messages = append(a.messages, components.Message{Role: "assistant"})
	m, _ := a.Update(streamChunkMsg{Done: true, Usage: &transcript.Usage{TotalTokens: 100}})
	a = m.(*App)
	if a.usageStale {
		t.Fatalf("fresh usage should clear staleness")
	}
	if a.messages[len(a.messages)-1].Usage == nil {
		t.Fatalf("assistant message should carry usage")
	}
	if a.footer.ContextStale {
		t.Fatalf("footer should not be stale after fresh usage")
	}
}

func TestModeCyclingSuppressesClassification(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	fc := &fakeClassifier{raw: "PLAN"}
	a.SetClassifier(fc)
	a.mode = "agent"

	a.cycleMode() // shift+tab
	if !a.modeExplicit {
		t.Fatalf("manual mode choice should set modeExplicit")
	}

	a.editor.SetValue("hello")
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if a.mode != "plan" {
		t.Fatalf("manual cycle should have moved agent→plan, got %q", a.mode)
	}
	if a.modeExplicit {
		t.Fatalf("modeExplicit should be consumed after the turn")
	}
}

// TestChatViewEditorOnOwnLine guards a regression where the editor was
// concatenated onto the viewport's last (width-padded) line, pushing typed
// text off the right edge of the terminal.
func TestChatViewEditorOnOwnLine(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	for _, r := range "hello" {
		a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	for _, line := range strings.Split(a.View(), "\n") {
		if !strings.Contains(line, "hello") {
			continue
		}
		if got := len(line) - len(strings.TrimLeft(line, " ")); got > 2 {
			t.Fatalf("editor line indented by %d columns, want <= 2: %q", got, line)
		}
		return
	}
	t.Fatal("typed text not rendered in chat view")
}

func TestEditorGrowsWithNewlines(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	base := a.editor.Height()
	a.editor.SetValue("line1\nline2\nline3\nline4")
	a.relayout()
	if a.editor.Height() <= base {
		t.Fatalf("editor should grow with newlines: %d vs base %d", a.editor.Height(), base)
	}
}

func TestEditorHeightClamps(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 80})
	a.editor.SetValue(strings.Repeat("x\n", 50))
	a.relayout()
	if a.editor.Height() > editorMaxHeight {
		t.Fatalf("editor height %d exceeds max %d", a.editor.Height(), editorMaxHeight)
	}
}

func TestBelowViewportHeightAccountsForFileAndPromptPickers(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	base := a.belowViewportHeight()

	// File picker visible.
	a.files = []string{"a.go", "b.go"}
	a.filesLoadedAt = time.Now()
	a.editor.SetValue("@")
	a.editor.CursorEnd()
	if !a.filePickerVisible() {
		t.Fatalf("file picker should be visible")
	}
	withFile := a.belowViewportHeight()
	if withFile <= base {
		t.Fatalf("belowViewportHeight with file picker (%d) should exceed base (%d)", withFile, base)
	}

	// Prompt picker visible.
	a = New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a.historyActive = true
	a.historyResults = []historyItem{{Name: "deploy", Prompt: "deploy the app"}}
	if !a.promptPickerVisible() {
		t.Fatalf("prompt picker should be visible")
	}
	withPrompt := a.belowViewportHeight()
	if withPrompt <= base {
		t.Fatalf("belowViewportHeight with prompt picker (%d) should exceed base (%d)", withPrompt, base)
	}
}

func TestChromeHeightMatchesRenderedView(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a.relayout()
	rendered := lipgloss.Height(a.View())
	if got := a.chromeHeight(); got != rendered-a.vp.Height {
		t.Fatalf("chromeHeight = %d, want %d (rendered=%d vp.Height=%d)", got, rendered-a.vp.Height, rendered, a.vp.Height)
	}
}

func TestCredentialViewSetsEnvReference(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.view = viewCredentials
	a.credentialState.backend = credentials.SourceUserFile
	a.credentialState.envMode = true
	a.editor.SetValue("MY_KEY")

	m, _ := a.handleCredentialKey(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if a.credentialState.envMode {
		t.Fatal("envMode should be cleared after enter")
	}

	t.Setenv("MY_KEY", "secret")
	v, origin, ok := resolver.Lookup("openai", "api_key")
	if !ok || v != "secret" {
		t.Fatalf("lookup = %q, %q, %v; want secret via env ref", v, origin, ok)
	}
	if origin != "$MY_KEY" {
		t.Fatalf("origin = %q, want $MY_KEY", origin)
	}
}

func TestBuildTurnsPreservesToolMetadata(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "user", Content: "run the tests"},
		{Role: "assistant", Content: "will do", ToolCalls: []components.AgentToolCall{
			{ID: "call_1", Name: "Bash", Args: `{"command":"go test ./..."}`},
		}},
		{Role: "tool", Content: "ok", ToolName: "Bash", ToolCallID: "call_1"},
	}
	turns := a.buildTurns()
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}
	if len(turns[1].ToolCalls) != 1 || turns[1].ToolCalls[0].Name != "Bash" {
		t.Fatalf("assistant turn lost tool calls: %+v", turns[1].ToolCalls)
	}
	if turns[2].Role != "tool" || turns[2].ToolCallID != "call_1" || turns[2].ToolName != "Bash" {
		t.Fatalf("tool turn metadata wrong: %+v", turns[2])
	}
}

// ---------------------------------------------------------------------------
// Prompt history / library tests
// ---------------------------------------------------------------------------

func TestHistoryCycleUpArrow(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "older prompt"})
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "newer prompt"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if !a.historyActive {
		t.Fatalf("expected historyActive after Up")
	}
	if a.editor.Value() != "newer prompt" {
		t.Fatalf("editor = %q, want newer prompt", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "older prompt" {
		t.Fatalf("editor = %q, want older prompt", a.editor.Value())
	}
}

func TestHistoryCycleDownArrowRestoresOriginal(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "history item"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "history item" {
		t.Fatalf("expected history item, got %q", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.historyActive {
		t.Fatalf("expected history cycle to exit")
	}
	if a.editor.Value() != "" {
		t.Fatalf("editor = %q, want empty", a.editor.Value())
	}
}

func TestHistoryCycleEnterAccepts(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "accepted prompt"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.historyActive {
		t.Fatalf("expected history cycle to exit after Enter")
	}
	// Enter now submits the loaded history item, which resets the editor.
	if a.editor.Value() != "" {
		t.Fatalf("editor = %q, want empty after submit", a.editor.Value())
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "user" && m.Content == "accepted prompt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("submitted history item not echoed: %+v", a.messages)
	}
}

func TestHistoryCycleEscCancels(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "history item"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("my text")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})

	if a.historyActive {
		t.Fatalf("expected history cycle to exit after Esc")
	}
	if a.editor.Value() != "my text" {
		t.Fatalf("editor = %q, want my text", a.editor.Value())
	}
}

// The composer text seeds the browse cycle, so a partial prompt still narrows
// what up offers; it is only mid-cycle typing that no longer re-filters.
func TestHistoryCycleSeedsFromComposer(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "how to deploy"})
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "unrelated"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("how to")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "how to deploy" {
		t.Fatalf("expected 'how to deploy', got %q", a.editor.Value())
	}
	if len(a.historyResults) != 1 {
		t.Fatalf("expected the seed to narrow the results, got %+v", a.historyResults)
	}
}

// Typing mid-cycle used to re-filter invisibly; it now leaves the cycle and
// edits the loaded prompt, which is what the composer already looked like it
// was doing.
func TestHistoryCycleTypingEditsLoadedPrompt(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "how to deploy"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})

	if a.historyActive {
		t.Fatalf("expected typing to leave the browse cycle")
	}
	if a.editor.Value() != "how to deploy!" {
		t.Fatalf("editor = %q, want the loaded prompt with the typed rune", a.editor.Value())
	}
}

func TestHistoryCycleTabCyclesNamedPrompts(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
		{Name: "review", Prompt: "review the diff"},
	}})

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "unnamed history"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if !a.promptPickerVisible() {
		t.Fatalf("expected the named-prompt strip to show")
	}
	if a.namedHistoryCount() != 2 {
		t.Fatalf("namedHistoryCount = %d, want 2", a.namedHistoryCount())
	}
	if a.editor.Value() != "deploy the app" {
		t.Fatalf("editor = %q, want the first named prompt", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.editor.Value() != "review the diff" {
		t.Fatalf("editor = %q, want the second named prompt", a.editor.Value())
	}

	// Tab wraps inside the named prefix rather than walking into the unnamed
	// session history, which up/down still reaches.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.editor.Value() != "deploy the app" {
		t.Fatalf("editor = %q, want tab to wrap to the first named prompt", a.editor.Value())
	}

	strip := a.renderPromptPicker()
	if !strings.Contains(strip, "deploy") || !strings.Contains(strip, "review") {
		t.Fatalf("strip missing prompt names: %q", strip)
	}
}

func TestHistoryCycleRightAccepts(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
	}})

	a := New(Options{Workdir: workdir})
	a.editor.SetValue("")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})

	if a.historyActive {
		t.Fatalf("expected right to leave the browse cycle")
	}
	if a.editor.Value() != "deploy the app" {
		t.Fatalf("editor = %q, want the accepted prompt", a.editor.Value())
	}
}

func TestLibraryRankedBeforeHistory(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	lib := promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
	}}
	_ = promptlib.SaveGlobal(lib)

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "deploy the app"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "deploy the app" {
		t.Fatalf("expected library prompt, got %q", a.editor.Value())
	}
}

// Tab moves the highlight through every candidate and wraps. It must not write
// into the prompt: doing so narrowed the candidate list to the written command
// on the next refresh, which pinned the cycle to one entry.
func TestAutocompleteTabCyclesWithoutTouchingThePrompt(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	want := a.autocomplete
	if len(want) < 3 {
		t.Fatalf("expected several autocomplete hints, got %v", want)
	}

	for i := range want {
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
		got, ok := a.autocompleteSelection()
		if !ok || got != want[i] {
			t.Fatalf("tab %d: selection = %q (ok=%v), want %q", i+1, got, ok, want[i])
		}
		if a.editor.Value() != "/c" {
			t.Fatalf("tab %d: prompt = %q, want it untouched", i+1, a.editor.Value())
		}
		if !slices.Equal(a.autocomplete, want) {
			t.Fatalf("tab %d: candidates = %v, want %v", i+1, a.autocomplete, want)
		}
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if got, _ := a.autocompleteSelection(); got != want[0] {
		t.Fatalf("selection after wrap = %q, want %q", got, want[0])
	}
}

// A refresh driven by an unrelated message (the cursor blink runs one per
// tick) must not disturb the highlight while the prompt is unchanged.
func TestAutocompleteHighlightSurvivesRefresh(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	want, _ := a.autocompleteSelection()

	a.refreshAutocomplete()

	if got, ok := a.autocompleteSelection(); !ok || got != want {
		t.Fatalf("selection after refresh = %q (ok=%v), want %q", got, ok, want)
	}
}

// Enter commits the highlighted candidate into the prompt rather than sending
// the turn; the popup closes and a second enter is what submits.
func TestAutocompleteEnterSelectsHighlight(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	want, _ := a.autocompleteSelection()
	before := len(a.messages)

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.editor.Value() != want {
		t.Fatalf("prompt = %q, want %q", a.editor.Value(), want)
	}
	if len(a.autocomplete) != 0 {
		t.Fatalf("expected the popup to close, got %v", a.autocomplete)
	}
	if len(a.messages) != before {
		t.Fatalf("enter submitted the turn: messages %d, want %d", len(a.messages), before)
	}
}

// Typing after a tab drops the highlight, so enter submits again instead of
// re-selecting a candidate the chip row no longer shows.
func TestAutocompleteTypingDropsHighlight(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})

	if _, ok := a.autocompleteSelection(); ok {
		t.Fatalf("expected the highlight to be dropped after typing")
	}
}

// Esc dismisses the highlight before it reaches the request, so a stray tab
// cannot turn an esc into a cancelled turn.
func TestAutocompleteEscClearsHighlightFirst(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/c")
	a.refreshAutocomplete()
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	before := len(a.messages)

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})

	if _, ok := a.autocompleteSelection(); ok {
		t.Fatalf("expected the highlight to be cleared")
	}
	if len(a.autocomplete) == 0 {
		t.Fatalf("expected the candidates to stay on screen")
	}
	if len(a.messages) != before {
		t.Fatalf("esc reached the request: messages %d, want %d", len(a.messages), before)
	}
}

func TestAutocompleteRightArrowAcceptsFirst(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("/cle")
	a.autocomplete = a.registry.Complete(a.editor.Value())
	if len(a.autocomplete) == 0 {
		t.Fatalf("expected autocomplete hints")
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.editor.Value() != "/clear" {
		t.Fatalf("expected /clear, got %q", a.editor.Value())
	}
	if len(a.autocomplete) != 0 {
		t.Fatalf("expected autocomplete cleared")
	}
}

func TestSavePromptMode(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	a := New(Options{Workdir: workdir})
	a.editor.SetValue("my favourite prompt")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyF7})
	if !a.savePromptMode {
		t.Fatalf("expected savePromptMode")
	}
	if a.editor.Value() != "" {
		t.Fatalf("editor should be cleared for naming")
	}

	for _, r := range "fave" {
		a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.savePromptMode {
		t.Fatalf("save mode should exit after Enter")
	}

	lib, err := promptlib.LoadProject(workdir)
	if err != nil {
		t.Fatalf("load project library: %v", err)
	}
	if len(lib.Entries) != 1 || lib.Entries[0].Name != "fave" || lib.Entries[0].Prompt != "my favourite prompt" {
		t.Fatalf("library = %+v", lib.Entries)
	}
}

func TestSavePromptModeEmptyNameCancels(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	a := New(Options{Workdir: workdir})
	a.editor.SetValue("prompt body")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true})

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.savePromptMode {
		t.Fatalf("save mode should exit after empty-name Enter")
	}
}

func TestSavePromptModeEscCancels(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	a := New(Options{Workdir: workdir})
	a.editor.SetValue("prompt body")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true})

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEscape})
	if a.savePromptMode {
		t.Fatalf("save mode should exit after Esc")
	}
}

func TestSavePromptModeEmptyPromptWarns(t *testing.T) {
	a := New(Options{})
	a.editor.SetValue("")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true})
	if a.savePromptMode {
		t.Fatalf("save mode should not start with empty prompt")
	}
}

func TestHistoryCycleBackspaceLeavesCycleAndEdits(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "abc"})
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "axyz"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("abc")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "abc" {
		t.Fatalf("expected 'abc', got %q", a.editor.Value())
	}

	// Backspace ends the browse cycle and deletes one rune of the loaded
	// prompt — it does not re-filter and swap the composer contents.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if a.historyActive {
		t.Fatalf("expected history cycle to exit on backspace")
	}
	if a.editor.Value() != "ab" {
		t.Fatalf("editor = %q, want %q", a.editor.Value(), "ab")
	}
}

func TestLibraryProjectOverridesGlobal(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "x", Prompt: "global x"},
	}})
	_ = promptlib.SaveProject(workdir, promptlib.Library{Entries: []promptlib.Entry{
		{Name: "x", Prompt: "project x"},
	}})

	a := New(Options{Workdir: workdir})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})

	if a.editor.Value() != "project x" {
		t.Fatalf("expected project override, got %q", a.editor.Value())
	}
}

// The result list is library entries first, then session history, and a
// history prompt identical to a library prompt is dropped so a saved prompt is
// offered once, under its name.
func TestHistoryResultsRankLibraryFirstAndDedupe(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
	}})

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "deploy the app"})
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "something else"})

	a := New(Options{Workdir: workdir})
	a.store = st

	got := a.buildHistoryResults("")
	want := []historyItem{
		{Name: "deploy", Prompt: "deploy the app"},
		{Prompt: "something else"},
	}
	if len(got) != len(want) {
		t.Fatalf("results = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("results[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A library entry matches on its name as well as its prompt text, so the
// composer seed can name the prompt it wants.
func TestHistoryResultsMatchOnName(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "ship it"},
		{Name: "review", Prompt: "read the diff"},
	}})

	a := New(Options{Workdir: workdir})
	got := a.buildHistoryResults("DEPLOY")
	if len(got) != 1 || got[0].Prompt != "ship it" {
		t.Fatalf("results = %+v, want the entry named deploy", got)
	}
}

// With no library entry in the results there is no strip to draw, and the
// composer hint drops the tab segment rather than advertising a key that does
// nothing.
func TestPromptPickerHiddenWithoutNamedPrompts(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "plain history"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})

	if a.promptPickerVisible() {
		t.Fatalf("strip should be hidden with no named prompts")
	}
	meta := a.renderComposer()
	if strings.Contains(meta, "tab name") {
		t.Fatalf("composer hint should not advertise tab: %q", meta)
	}
	if !strings.Contains(meta, "esc cancel") {
		t.Fatalf("composer hint should still show the browse keys: %q", meta)
	}

	// Tab has nothing to cycle and must leave the loaded prompt alone.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.editor.Value() != "plain history" {
		t.Fatalf("editor = %q, want the loaded history prompt", a.editor.Value())
	}
}

func TestPromptPickerHintShownWithNamedPrompts(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
	}})

	a := New(Options{Workdir: workdir})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})

	if !strings.Contains(a.renderComposer(), "tab name") {
		t.Fatalf("composer hint should advertise tab while a strip is showing")
	}
	if !strings.Contains(a.View(), "deploy") {
		t.Fatalf("frame should carry the named-prompt strip")
	}
}

// up walks past the named prefix into unnamed session history; tab returns to
// the first named prompt rather than continuing into the unnamed tail.
func TestHistoryTabReturnsFromUnnamedTail(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	_ = promptlib.SaveGlobal(promptlib.Library{Entries: []promptlib.Entry{
		{Name: "deploy", Prompt: "deploy the app"},
	}})

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "unnamed history"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "unnamed history" {
		t.Fatalf("editor = %q, want the unnamed history entry", a.editor.Value())
	}
	if a.historyIndex != 1 {
		t.Fatalf("historyIndex = %d, want 1", a.historyIndex)
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.editor.Value() != "deploy the app" {
		t.Fatalf("editor = %q, want tab to return to the first named prompt", a.editor.Value())
	}
}

// An empty result set still opens the cycle with the typed text intact, and
// esc restores it untouched.
func TestHistoryCycleNoResultsKeepsTypedText(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	a := New(Options{Workdir: workdir})
	a.editor.SetValue("nothing matches this")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if !a.historyActive {
		t.Fatalf("expected the cycle to open even with no results")
	}
	if a.historyIndex != -1 {
		t.Fatalf("historyIndex = %d, want -1", a.historyIndex)
	}
	if a.editor.Value() != "nothing matches this" {
		t.Fatalf("editor = %q, want the typed text", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.editor.Value() != "nothing matches this" {
		t.Fatalf("editor = %q, want the typed text untouched", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEscape})
	if a.historyActive || a.editor.Value() != "nothing matches this" {
		t.Fatalf("esc should restore the typed text, got %q", a.editor.Value())
	}
}

// The filter is fixed for the life of a cycle: the composer seed narrows the
// results, and nothing rebuilds them until the cycle is re-entered.
func TestHistoryCycleFilterIsFixedForTheCycle(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "how to deploy"})
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "unrelated"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("how to")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if len(a.historyResults) != 1 {
		t.Fatalf("results = %+v, want only the seeded match", a.historyResults)
	}
	// up past the end holds on the last result rather than wrapping or
	// reloading the unfiltered list.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "how to deploy" {
		t.Fatalf("editor = %q, want the single match held", a.editor.Value())
	}
}

// ---------------------------------------------------------------------------
// Output truncation and expansion tests
// ---------------------------------------------------------------------------

func TestCtrlOTogglesExpandAll(t *testing.T) {
	a := New(Options{})
	if a.expandAll {
		t.Fatalf("expandAll should default to false")
	}
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	a = m.(*App)
	if !a.expandAll {
		t.Fatalf("first ctrl+o should expand all")
	}
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	a = m.(*App)
	if a.expandAll {
		t.Fatalf("second ctrl+o should collapse all")
	}
}

func TestCtrlOOnlyWorksInChatView(t *testing.T) {
	a := New(Options{})
	a.push(viewSettings)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	a = m.(*App)
	if a.expandAll {
		t.Fatalf("ctrl+o should not expand all while in settings view")
	}
}

func TestToolResultStoresContentAndSetsErrorStatus(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "assistant"},
		{Role: "tool", ToolName: "Bash", ToolCallID: "call_1"},
	}
	_, cmd := a.Update(agentEventMsg{Kind: agent.EventToolResultKind, ToolName: "Bash", ToolResult: "exit status 1"})
	if cmd == nil {
		t.Fatalf("expected follow-up command to keep draining events")
	}
	last := a.messages[len(a.messages)-1]
	if last.Content != "exit status 1" {
		t.Fatalf("tool content = %q, want exit status 1", last.Content)
	}
	if last.Status != "✗" {
		t.Fatalf("tool status = %q, want ✗", last.Status)
	}
}

func TestToolResultStoresContentAndSetsWithheldStatus(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "assistant"},
		{Role: "tool", ToolName: "Read", ToolCallID: "call_1"},
	}
	_, cmd := a.Update(agentEventMsg{Kind: agent.EventToolResultKind, ToolName: "Read", ToolResult: "tool result withheld: permission denied"})
	if cmd == nil {
		t.Fatalf("expected follow-up command to keep draining events")
	}
	last := a.messages[len(a.messages)-1]
	if last.Content != "tool result withheld: permission denied" {
		t.Fatalf("tool content = %q", last.Content)
	}
	if last.Status != "withheld" {
		t.Fatalf("tool status = %q, want withheld", last.Status)
	}
}

func TestToolResultStoresContentAndSetsSuccessStatus(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "assistant"},
		{Role: "tool", ToolName: "Bash", ToolCallID: "call_1"},
	}
	_, cmd := a.Update(agentEventMsg{Kind: agent.EventToolResultKind, ToolName: "Bash", ToolResult: "hello"})
	if cmd == nil {
		t.Fatalf("expected follow-up command to keep draining events")
	}
	last := a.messages[len(a.messages)-1]
	if last.Content != "hello" {
		t.Fatalf("tool content = %q, want hello", last.Content)
	}
	if last.Status != "✓" {
		t.Fatalf("tool status = %q, want ✓", last.Status)
	}
}

func TestEnterWhileWorkingSteers(t *testing.T) {
	a := New(Options{})
	a.cancel = func() {}
	a.agent = &agent.Session{}
	a.editor.SetValue("keep going")

	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("steering must not start a new send, got cmd %v", cmd)
	}
	if len(a.messages) == 0 {
		t.Fatal("expected a steering message")
	}
	var found bool
	for _, m := range a.messages {
		if m.Role == "user" && m.Steering && m.Content == "keep going" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected steering user message, got %+v", a.messages)
	}
	if a.editor.Value() != "" {
		t.Fatalf("editor should reset after steering, got %q", a.editor.Value())
	}
}

// --- instant echo and the Role Manager working indicator ------------------

func TestSubmitInputEchoesPromptInstantly(t *testing.T) {
	a := New(Options{})
	a.mode = "goal" // avoid the agent-picker gate during pre-send tests
	a.modeSticky = false
	a.SetClassifier(&fakeClassifier{raw: "AGENT"})
	a.editor.SetValue("hello world")

	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected the async classify command")
	}
	var echoed bool
	for _, m := range a.messages {
		if m.Role == "user" && m.Content == "hello world" {
			echoed = true
		}
	}
	if !echoed {
		t.Fatalf("expected an instant 'user prompt' echo, got %+v", a.messages)
	}
	if a.phase != phaseRoleManager {
		t.Fatalf("phase = %d, want phaseRoleManager while the classifier runs", a.phase)
	}
	if !a.preSend {
		t.Fatal("expected preSend to be set while the classifier runs")
	}
	for _, m := range a.messages {
		if m.Role == "assistant" {
			t.Fatal("no assistant bubble may appear before the turn starts")
		}
	}
	if a.editor.Value() != "" {
		t.Fatalf("editor should be cleared on submit, got %q", a.editor.Value())
	}
}

func TestSubmitInputClassifiesAsyncThenSends(t *testing.T) {
	srv := newAgentSSEServer(t, "pong")
	defer srv.Close()

	src := &fakeCredentialSource{vals: map[string]string{"openai:api_key": "sk-test"}}
	cfg, status := run.Prepare("gpt-5", "openai", src)
	if !status.Configured {
		t.Fatalf("expected configured")
	}
	cfg.BaseURL = srv.URL
	a := New(Options{Client: srv.Client(), Provider: "openai", Model: "gpt-5", Workdir: t.TempDir()})
	a.mode = "goal" // avoid the agent-picker gate during pre-send tests
	a.modeSticky = false
	a.cfg = cfg
	a.status = status
	a.SetClassifier(&fakeClassifier{raw: "PLAN"})
	a.sessionName = "named" // skip the auto-naming side channel
	a.editor.SetValue("refactor the parser")

	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected the async classify command")
	}
	// submitInput batches the classify command with the work-spinner tick;
	// find the classify message among them.
	raw := cmd()
	var classified modeClassifiedMsg
	switch msg := raw.(type) {
	case modeClassifiedMsg:
		classified = msg
	case tea.BatchMsg:
		for _, c := range msg {
			if m := c(); m != nil {
				if cm, ok := m.(modeClassifiedMsg); ok {
					classified = cm
					break
				}
			}
		}
	}
	if classified.input == "" {
		t.Fatalf("classify command produced %T, want modeClassifiedMsg", raw)
	}
	m, next := a.Update(classified)
	a = m.(*App)
	if a.mode != "plan" {
		t.Fatalf("mode = %q, want plan", a.mode)
	}
	if a.preSend {
		t.Fatal("preSend must clear once the decision lands")
	}
	if next == nil {
		t.Fatal("expected the send command once the decision lands")
	}
	a = drainAgent(t, a, next)
	if a.phase != phaseIdle {
		t.Fatalf("phase = %d after done, want phaseIdle", a.phase)
	}
	userCount, assistantCount := 0, 0
	for _, m := range a.messages {
		switch m.Role {
		case "user":
			userCount++
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				assistantCount++
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("user messages = %d, want 1 (echoed once, not duplicated)", userCount)
	}
	if assistantCount != 1 {
		t.Fatalf("assistant messages = %d, want 1, got %+v", assistantCount, a.messages)
	}
	last := a.trailingAssistant()
	if last < 0 || a.messages[last].Content != "pong" {
		t.Fatalf("expected one 'pong' assistant reply, got %+v", a.messages)
	}
	// Plan mode presents its own evaluator verdict, not a goal evaluator.
	var hasPlanEval bool
	var hasPlanFile bool
	for _, m := range a.messages {
		if strings.Contains(m.Content, "plan evaluator: PLAN_COMPLETE") {
			hasPlanEval = true
		}
		if strings.Contains(m.Content, ".vulnetix/plans/") {
			hasPlanFile = true
		}
	}
	if !hasPlanEval {
		t.Fatalf("expected a plan evaluator verdict, got %+v", a.messages)
	}
	if !hasPlanFile {
		t.Fatalf("expected a plan file system message, got %+v", a.messages)
	}
}

func TestEscCancelsPreSend(t *testing.T) {
	a := New(Options{})
	a.mode = "goal" // avoid the agent-picker gate during pre-send tests
	a.modeSticky = false
	a.SetClassifier(&fakeClassifier{raw: "AGENT"})
	a.editor.SetValue("hello")
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected the async classify command")
	}

	m, cancelCmd := a.Update(tea.KeyMsg{Type: tea.KeyEscape})
	a = m.(*App)
	if cancelCmd != nil {
		t.Fatalf("esc during pre-send must not start work, got %v", cancelCmd)
	}
	if a.preSend {
		t.Fatal("esc must clear preSend")
	}
	if a.phase != phaseIdle {
		t.Fatalf("phase = %d, want phaseIdle", a.phase)
	}
	// The late classification result must not send the turn.
	m, sendCmd := a.Update(cmd())
	a = m.(*App)
	if sendCmd != nil {
		t.Fatalf("late classification must not send, got %v", sendCmd)
	}
	if a.working() {
		t.Fatal("a cancelled pre-send must not start a turn")
	}
}

func TestEnterDuringPreSendDoesNotSendOrSteer(t *testing.T) {
	a := New(Options{})
	a.mode = "goal" // avoid the agent-picker gate during pre-send tests
	a.modeSticky = false
	a.SetClassifier(&fakeClassifier{raw: "AGENT"})
	a.editor.SetValue("first")
	if cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("expected the async classify command")
	}
	before := len(a.messages)

	a.editor.SetValue("second")
	if again := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}); again != nil {
		t.Fatalf("enter during pre-send must not start work, got %v", again)
	}
	if len(a.messages) != before+1 {
		t.Fatalf("expected exactly one hint line, got %+v", a.messages)
	}
	hint := a.messages[before]
	if hint.Role != "system" || !strings.Contains(hint.Content, "still preparing") {
		t.Fatalf("expected a 'still preparing' system hint, got %+v", hint)
	}
}

func TestComposerRoleManagerIndicator(t *testing.T) {
	a := New(Options{})
	a.width, a.height = 100, 30
	a.setPhaseRoleManager(agent.RoleManagerPhasePrePrompt)

	view := a.renderComposer()
	if !strings.Contains(view, "role manager") {
		t.Fatalf("composer must carry the role manager signal:\n%s", view)
	}
	if !strings.Contains(view, "pre-prompt processing") {
		t.Fatalf("composer must caption the sub-phase:\n%s", view)
	}
	if strings.Contains(view, "working") {
		t.Fatalf("the generic 'working' label must not appear for Role Manager activity:\n%s", view)
	}
}

func TestComposerWorkingLabelForGenericIO(t *testing.T) {
	a := New(Options{})
	a.width, a.height = 100, 30
	a.setPhaseWorking()

	view := a.renderComposer()
	if !strings.Contains(view, "working") {
		t.Fatalf("composer must show the generic working label for plain I/O:\n%s", view)
	}
	if strings.Contains(view, "role manager") {
		t.Fatalf("the role manager signal must not appear for generic I/O:\n%s", view)
	}
}

func TestRMCaptionSubPhases(t *testing.T) {
	a := New(Options{})
	cases := []struct {
		phase, want string
	}{
		{agent.RoleManagerPhasePrePrompt, "pre-prompt processing"},
		{agent.RoleManagerPhaseToolResult, "classifying tool result"},
		{agent.RoleManagerPhaseSteer, "classifying steering"},
		{"", "pre-prompt processing"},
	}
	for _, c := range cases {
		a.setPhaseRoleManager(c.phase)
		if got := a.rmCaption(); got != c.want {
			t.Fatalf("rmCaption(%q) = %q, want %q", c.phase, got, c.want)
		}
	}
}

func TestAgentEventsDriveWorkingPhase(t *testing.T) {
	a := New(Options{})
	a.setPhaseRoleManager(agent.RoleManagerPhasePrePrompt)

	step := func(m tea.Msg) {
		t.Helper()
		var next tea.Cmd
		var mm tea.Model
		mm, next = a.Update(m)
		a = mm.(*App)
		_ = next
	}
	step(agentEventMsg{Kind: agent.EventRoleManagerKind, Phase: agent.RoleManagerPhaseToolResult})
	if a.phase != phaseRoleManager || a.rmPhase != agent.RoleManagerPhaseToolResult {
		t.Fatalf("RM event must keep the role manager phase, got %d/%q", a.phase, a.rmPhase)
	}
	step(agentEventMsg{Kind: agent.EventTextKind, Text: "hi"})
	if a.phase != phaseWorking {
		t.Fatalf("streaming text must switch to the generic working phase, got %d", a.phase)
	}
	step(agentEventMsg{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{Name: "Read"}})
	if a.phase != phaseWorking {
		t.Fatalf("tool execution must be generic working I/O, got %d", a.phase)
	}
	step(agentEventMsg{Kind: agent.EventRoleManagerKind, Phase: agent.RoleManagerPhaseToolResult})
	if a.phase != phaseRoleManager {
		t.Fatalf("tool-result classification must re-enter the role manager phase, got %d", a.phase)
	}
	step(agentEventMsg{Kind: agent.EventDoneKind})
	if a.phase != phaseIdle {
		t.Fatalf("done must clear the phase, got %d", a.phase)
	}
}

func TestSpinMarkHonoursSpinnerSetting(t *testing.T) {
	a := New(Options{})
	if got := a.spinMark(); got == "•" {
		t.Fatal("spinner default-on must render the spinner frame, not the static dot")
	}
	f := false
	a.settings.UI = &config.UISettings{Spinner: &f}
	if got := a.spinMark(); got != "•" {
		t.Fatalf("spinner off must render the static dot, got %q", got)
	}
}

func TestComposerExploringIndicator(t *testing.T) {
	a := New(Options{})
	a.width, a.height = 100, 30
	a.setPhaseExploring(1, 3, "repository structure")

	view := a.renderComposer()
	if !strings.Contains(view, "explore") {
		t.Fatalf("composer must carry the explore pill:\n%s", view)
	}
	if !strings.Contains(view, "exploring 1/3") {
		t.Fatalf("composer must caption the exploring progress:\n%s", view)
	}
	if strings.Contains(view, "role manager") {
		t.Fatalf("explore is not a Role Manager signal:\n%s", view)
	}
	if strings.Contains(view, "working") {
		t.Fatalf("explore must not use the generic working label:\n%s", view)
	}
}

func TestF8TogglesSubagentStripFocus(t *testing.T) {
	a := New(Options{})
	a.subagents = []components.SubagentChip{{ID: "e1", Label: "x", State: "done"}}
	a.subagentIdx = map[string]int{"e1": 0}

	a.Update(tea.KeyMsg{Type: tea.KeyF8})
	if !a.stripFocus {
		t.Fatal("f8 must focus the strip when a roster exists")
	}
	if a.stripSel != 0 {
		t.Fatalf("stripSel = %d, want 0 (main)", a.stripSel)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF8})
	if a.stripFocus {
		t.Fatal("f8 must toggle the strip focus off")
	}
}

func TestF8NoopWithEmptyRoster(t *testing.T) {
	a := New(Options{})
	a.Update(tea.KeyMsg{Type: tea.KeyF8})
	if a.stripFocus {
		t.Fatal("f8 must be a no-op with an empty roster")
	}
}

func TestSubagentStripCyclesAndFilters(t *testing.T) {
	a := New(Options{})
	a.subagents = []components.SubagentChip{{ID: "e1", Label: "x", State: "done"}, {ID: "e2", Label: "y", State: "done"}}
	a.subagentIdx = map[string]int{"e1": 0, "e2": 1}
	a.stripFocus = true
	a.stripSel = 0

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.stripSel != 1 {
		t.Fatalf("right = %d, want 1 (e1)", a.stripSel)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight})
	if a.stripSel != 2 {
		t.Fatalf("right = %d, want 2 (e2)", a.stripSel)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRight}) // clamped at the end
	if a.stripSel != 2 {
		t.Fatalf("right past the end must clamp, got %d", a.stripSel)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyLeft})
	if a.stripSel != 1 {
		t.Fatalf("left = %d, want 1", a.stripSel)
	}

	// enter on e1 filters the transcript.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.threadFilter != "e1" {
		t.Fatalf("threadFilter = %q, want e1", a.threadFilter)
	}

	// enter on main clears the filter.
	a.stripSel = 0
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.threadFilter != "" {
		t.Fatalf("threadFilter = %q, want cleared", a.threadFilter)
	}
}

func TestSubagentStripXCancelsAndDismisses(t *testing.T) {
	a := New(Options{})
	a.agentPool = agentpool.New(1)
	a.subagents = []components.SubagentChip{{ID: "e1", Label: "x", State: "running"}}
	a.subagentIdx = map[string]int{"e1": 0}
	a.stripFocus = true
	a.stripSel = 1

	// x on a running chip cancels but never removes it.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if len(a.subagents) != 1 {
		t.Fatal("running chip must not be dismissed")
	}
	if _, ok := a.subagentIdx["e1"]; !ok {
		t.Fatal("running chip must keep its roster entry")
	}

	// x on a terminal chip dismisses it.
	a.subagents[0].State = "done"
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if len(a.subagents) != 0 {
		t.Fatalf("terminal chip must be dismissed, got %d", len(a.subagents))
	}
	if _, ok := a.subagentIdx["e1"]; ok {
		t.Fatal("dismissed chip must lose its index entry")
	}
}

func TestFilteredViewHidesMainRows(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "user", Content: "prompt"},
		{Role: "tool", SubagentID: "e1", ToolName: "Read", Content: "subagent output"},
		{Role: "assistant", Content: "reply"},
	}
	a.threadFilter = "e1"

	msgs := a.filteredMessages()
	if len(msgs) != 2 {
		t.Fatalf("filtered messages = %d, want banner + one subagent row", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "filtered") {
		t.Fatalf("first row must be the filter banner, got %+v", msgs[0])
	}
	if msgs[1].SubagentID != "e1" {
		t.Fatalf("surviving row must be the filtered subagent's, got %+v", msgs[1])
	}
}

func TestBuildTurnsDropsSubagentRows(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "user", Content: "prompt"},
		{Role: "tool", SubagentID: "e1", ToolName: "Read", ToolCallID: "c1", Content: "raw subagent output"},
		{Role: "assistant", Content: "reply"},
	}

	turns := a.buildTurns()
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (user + assistant)", len(turns))
	}
	for _, tn := range turns {
		if strings.Contains(tn.Content, "raw subagent output") {
			t.Fatalf("subagent row leaked into provider turns: %+v", turns)
		}
	}
}

func TestHistoryCycleBackspaceEditsLoadedPrompt(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "history item"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "history item" {
		t.Fatalf("expected history item, got %q", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if a.historyActive {
		t.Fatalf("expected history cycle to exit on backspace")
	}
	if a.editor.Value() != "history ite" {
		t.Fatalf("editor = %q, want %q", a.editor.Value(), "history ite")
	}
}

func TestHistoryCycleBackspaceAfterFilterEditsLoadedPrompt(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "how to deploy"})

	a := New(Options{Workdir: workdir})
	a.store = st
	a.editor.SetValue("how")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	if a.editor.Value() != "how to deploy" {
		t.Fatalf("expected how to deploy, got %q", a.editor.Value())
	}

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if a.historyActive {
		t.Fatalf("expected history cycle to exit on backspace")
	}
	if a.editor.Value() != "how to deplo" {
		t.Fatalf("editor = %q, want %q", a.editor.Value(), "how to deplo")
	}
}

func TestHistoryCycleLeftArrowKeepsLoadedPrompt(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "history item"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyLeft})

	if a.historyActive {
		t.Fatalf("expected history cycle to exit on left arrow")
	}
	if a.editor.Value() != "history item" {
		t.Fatalf("editor = %q, want history item", a.editor.Value())
	}
}

func TestHistoryCycleEditAfterLeftArrowInsertsAtCursor(t *testing.T) {
	workdir := t.TempDir()
	t.Setenv("SIGNET_HOME", t.TempDir())

	st, _ := session.NewStore()
	_ = st.Append(workdir, "sess-1", session.Entry{Type: "user", Role: "user", Content: "abcd"})

	a := New(Options{Workdir: workdir})
	a.store = st

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyUp})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyLeft})
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})

	if a.editor.Value() != "abcXd" {
		t.Fatalf("editor = %q, want abcXd", a.editor.Value())
	}
}

func TestContextEstimateMemoised(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}

	first := a.contextEstimate()
	if !a.estInit || a.estKey == "" {
		t.Fatalf("estimate not initialised: estInit=%v estKey=%q", a.estInit, a.estKey)
	}
	key := a.estKey

	// Unchanged transcript: same key, same estimate, no recompute.
	if got := a.contextEstimate(); got != first {
		t.Fatalf("estimate changed on unchanged transcript: %+v != %+v", got, first)
	}
	if a.estKey != key {
		t.Fatalf("estimate key changed on unchanged transcript: %q != %q", a.estKey, key)
	}

	// A streaming delta grows the tail content: the key changes and the
	// estimate is recomputed to a larger value.
	a.messages[len(a.messages)-1].Content = "hi there, this is a much longer reply"
	second := a.contextEstimate()
	if a.estKey == key {
		t.Fatalf("estimate key did not change after tail grew: %q", a.estKey)
	}
	if second.Tokens <= first.Tokens {
		t.Fatalf("estimate did not grow with content: %+v <= %+v", second, first)
	}
}

// TestNextAgentCoalescesDeltas pins the drain-with-coalesce contract: a run of
// same-kind text (or reasoning) deltas arrives as one event, a different-kind
// event is stashed as lookahead and replayed in order, and a closed channel
// synthesises the done event.
func TestNextAgentCoalescesDeltas(t *testing.T) {
	a := New(Options{})
	ch := make(chan agent.Event, 8)
	a.events = ch

	ch <- agent.Event{Kind: agent.EventTextKind, Text: "hello "}
	ch <- agent.Event{Kind: agent.EventTextKind, Text: "world"}
	ch <- agent.Event{Kind: agent.EventReasoningKind, Reasoning: "thi"}
	ch <- agent.Event{Kind: agent.EventReasoningKind, Reasoning: "nking"}
	ch <- agent.Event{Kind: agent.EventToolStartKind}
	close(ch)

	next := func() agent.Event {
		cmd := a.nextAgent()
		return agent.Event(cmd().(agentEventMsg))
	}

	if e := next(); e.Kind != agent.EventTextKind || e.Text != "hello world" {
		t.Fatalf("first = %+v, want text 'hello world'", e)
	}
	if e := next(); e.Kind != agent.EventReasoningKind || e.Reasoning != "thinking" {
		t.Fatalf("second = %+v, want reasoning 'thinking'", e)
	}
	if e := next(); e.Kind != agent.EventToolStartKind {
		t.Fatalf("third = %+v, want tool start", e)
	}
	if e := next(); e.Kind != agent.EventDoneKind {
		t.Fatalf("fourth = %+v, want done", e)
	}
}

func TestHandleAgentReadyDropsCancelledBuild(t *testing.T) {
	a := New(Options{})
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.cancel()

	if cmd := a.handleAgentReady(agentReadyMsg{}); cmd != nil {
		t.Fatalf("cancelled session build must be dropped, got cmd %v", cmd)
	}
}

func TestHandleAgentReadySurfacesBuildError(t *testing.T) {
	a := New(Options{})
	a.ctx, a.cancel = context.WithCancel(context.Background())

	if cmd := a.handleAgentReady(agentReadyMsg{err: errors.New("boom")}); cmd != nil {
		t.Fatalf("build error should return nil cmd, got %v", cmd)
	}
	if a.phase != phaseIdle {
		t.Fatalf("phase after build error = %d, want idle", a.phase)
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Text(), "boom") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected build error surfaced as system message, got %v", a.messages)
	}
}

func TestResolveCredentialsCmdUsesResolver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	workdir := t.TempDir()

	userPath, err := config.UserCredentialsPath()
	if err != nil {
		t.Fatalf("UserCredentialsPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(userPath, []byte(`{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"sk-resolved"}}}}`), 0o600); err != nil {
		t.Fatalf("write user credentials: %v", err)
	}

	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.requestedProvider = "openai"

	msg := a.resolveCredentialsCmd()()
	rm, ok := msg.(credentialsResolvedMsg)
	if !ok {
		t.Fatalf("resolveCredentialsCmd = %T, want credentialsResolvedMsg", msg)
	}
	if !rm.status.Configured || rm.cfg.APIKey != "sk-resolved" {
		t.Fatalf("resolved cfg = %+v status=%+v, want configured with sk-resolved", rm.cfg, rm.status)
	}

	if cmd := a.handleCredentialsResolved(rm); cmd != nil {
		t.Fatalf("handleCredentialsResolved returned cmd %v for no pending prompt", cmd)
	}
	if !a.status.Configured || a.cfg.APIKey != "sk-resolved" {
		t.Fatalf("app not configured after resolution: %+v", a.status)
	}
}

// TestLocalModelDownloadCommand pins the /local-model download flow end to end:
// metadata resolution, checksummed download with progress, and a completion
// system notice.
