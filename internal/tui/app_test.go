package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestNewAppView(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	if a == nil {
		t.Fatalf("NewApp returned nil")
	}
	if v := a.View(); v == "" {
		t.Fatalf("View returned empty string")
	}
}

type fakeClassifier struct {
	raw string
}

func (f fakeClassifier) Classify(rolemanager.ClassifierPayload) (string, error) {
	return f.raw, nil
}

func TestClassifyModeSelectsPlan(t *testing.T) {
	a := NewApp(t.TempDir(), "")
	a.SetClassifier(fakeClassifier{raw: "PLAN"})

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
	a.SetClassifier(fakeClassifier{raw: "GOAL"})

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
	a.SetClassifier(fakeClassifier{raw: "AGENT"})

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
	// New may already have posted a "credentials missing" notice; classifyMode
	// must not add to whatever is there.
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
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLOUDFLARE_API_KEY", "")
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
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLOUDFLARE_API_KEY", "")
	called := false
	transport := &fatalTransport{t: t, called: &called}
	client := &http.Client{Transport: transport}
	a := New(Options{Client: client})

	// Simulate pressing enter
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	cmd := a.send(a.buildTurns())
	msg := cmd()
	chunk := msg.(streamChunkMsg)
	if chunk.Err == nil {
		t.Fatalf("expected error for missing credentials")
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
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

	cmd := a.send([]run.Turn{{Role: "user", Content: "ping"}})
	// Execute the command synchronously to start the stream
	msg := cmd()
	// Now pump messages until Done
	for {
		chunk := msg.(streamChunkMsg)
		if chunk.Done || chunk.Err != nil {
			break
		}
		m, nextCmd := a.Update(chunk)
		a = m.(*App)
		if nextCmd == nil {
			break
		}
		msg = nextCmd()
	}
	if len(a.messages) == 0 || a.messages[len(a.messages)-1].Content != "pong" {
		t.Fatalf("expected assistant reply 'pong', got %v", a.messages)
	}
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
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

	// First turn
	a.messages = append(a.messages, components.Message{Role: "user", Content: "ping"})
	cmd := a.send(a.buildTurns())
	msg := cmd()
	for {
		chunk := msg.(streamChunkMsg)
		if chunk.Done || chunk.Err != nil {
			break
		}
		m, nextCmd := a.Update(chunk)
		a = m.(*App)
		if nextCmd == nil {
			break
		}
		msg = nextCmd()
	}

	// Second turn
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
