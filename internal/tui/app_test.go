package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/credentials"
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
	if a.messages[len(a.messages)-1].Content != "hello" {
		t.Fatalf("content = %q", a.messages[len(a.messages)-1].Content)
	}

	m, _ = a.Update(streamChunkMsg{Text: " world"})
	a = m.(*App)
	if a.messages[len(a.messages)-1].Content != "hello world" {
		t.Fatalf("content = %q", a.messages[len(a.messages)-1].Content)
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
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(agentEventMsg); !ok {
			t.Fatalf("unexpected message %T", msg)
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

func TestChromeHeightMatchesRenderedView(t *testing.T) {
	a := New(Options{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a.relayout()
	want := a.height - a.vp.Height
	if a.chromeHeight() != want {
		t.Fatalf("chromeHeight = %d, want %d (height=%d vp.Height=%d)", a.chromeHeight(), want, a.height, a.vp.Height)
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
