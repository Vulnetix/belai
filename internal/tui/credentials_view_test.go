package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/credentials"
)

func credentialApp(t *testing.T, providers ...string) *App {
	t.Helper()
	a := New(Options{})
	a.view = viewCredentials
	a.credentialState.providers = providers
	return a
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestCredentialFieldCursorMoves(t *testing.T) {
	a := credentialApp(t, "cloudflare-workers-ai")

	a.Update(runeKey('l'))
	if a.credentialState.fieldIdx != 1 {
		t.Fatalf("fieldIdx after right = %d, want 1", a.credentialState.fieldIdx)
	}
	a.Update(runeKey('l'))
	if a.credentialState.fieldIdx != 1 {
		t.Fatalf("fieldIdx must clamp at last field, got %d", a.credentialState.fieldIdx)
	}
	a.Update(runeKey('h'))
	if a.credentialState.fieldIdx != 0 {
		t.Fatalf("fieldIdx after left = %d, want 0", a.credentialState.fieldIdx)
	}
	a.Update(runeKey('h'))
	if a.credentialState.fieldIdx != 0 {
		t.Fatalf("fieldIdx must clamp at first field, got %d", a.credentialState.fieldIdx)
	}
}

func TestCredentialFieldCursorIgnoresArrows(t *testing.T) {
	a := credentialApp(t, "cloudflare-workers-ai")
	a.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if a.credentialState.fieldIdx != 0 {
		t.Fatalf("fieldIdx = %d, want 0 (already first)", a.credentialState.fieldIdx)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.credentialState.fieldIdx != 1 {
		t.Fatalf("fieldIdx = %d, want 1 (arrow right)", a.credentialState.fieldIdx)
	}
}

func TestCredentialFieldCursorResetsOnProviderChange(t *testing.T) {
	a := credentialApp(t, "cloudflare-workers-ai", "cloudflare-ai-gateway")
	a.Update(runeKey('l'))
	if a.credentialState.fieldIdx != 1 {
		t.Fatalf("fieldIdx = %d, want 1 before provider change", a.credentialState.fieldIdx)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.credentialState.selectedIdx != 1 {
		t.Fatalf("selectedIdx = %d, want 1", a.credentialState.selectedIdx)
	}
	if a.credentialState.fieldIdx != 0 {
		t.Fatalf("fieldIdx = %d, want 0 after provider change", a.credentialState.fieldIdx)
	}
}

func TestCredentialSetWritesSelectedField(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_API_KEY", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.view = viewCredentials
	a.credentialState.providers = []string{"cloudflare-workers-ai"}
	a.credentialState.backend = credentials.SourceUserFile

	a.Update(runeKey('l')) // select account_id
	a.Update(runeKey('s'))
	a.editor.SetValue("acct-123")
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)

	if v, origin, ok := resolver.Lookup("cloudflare-workers-ai", "account_id"); !ok || v != "acct-123" {
		t.Fatalf("account_id lookup = %q, %q, %v; want acct-123 via user file", v, origin, ok)
	}
	if _, _, ok := resolver.Lookup("cloudflare-workers-ai", "api_key"); ok {
		t.Fatalf("api_key must not be written when account_id is selected")
	}
}

func TestCredentialEnvRefWritesSelectedField(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_API_KEY", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("MY_ACCT", "")
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.view = viewCredentials
	a.credentialState.providers = []string{"cloudflare-workers-ai"}
	a.credentialState.backend = credentials.SourceUserFile

	a.Update(runeKey('l')) // select account_id
	a.Update(runeKey('e'))
	a.editor.SetValue("MY_ACCT")
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)

	t.Setenv("MY_ACCT", "acct-from-env")
	if v, origin, ok := resolver.Lookup("cloudflare-workers-ai", "account_id"); !ok || v != "acct-from-env" {
		t.Fatalf("account_id lookup = %q, %q, %v; want acct-from-env via $MY_ACCT", v, origin, ok)
	}
	if _, _, ok := resolver.Lookup("cloudflare-workers-ai", "api_key"); ok {
		t.Fatalf("api_key must not be written when account_id is selected")
	}
}

func TestCredentialViewMarksSelectedField(t *testing.T) {
	a := credentialApp(t, "cloudflare-workers-ai")
	a.credentialState.fieldIdx = 1
	view := a.credentialView()

	var accountLine, apiKeyLine string
	for _, line := range strings.Split(view, "\n") {
		switch {
		case strings.Contains(line, "account_id"):
			accountLine = line
		case strings.Contains(line, "api_key"):
			apiKeyLine = line
		}
	}
	if accountLine == "" || apiKeyLine == "" {
		t.Fatalf("view should list both fields:\n%s", view)
	}
	// The renderer downgrades the cursor glyph on ASCII-only terminals, so
	// assert that the selected line carries a marker and the other does not,
	// rather than pinning the glyph itself.
	if strings.HasPrefix(strings.TrimLeft(accountLine, " "), "account_id") {
		t.Fatalf("selected field line lacks cursor: %q", accountLine)
	}
	if !strings.HasPrefix(strings.TrimLeft(apiKeyLine, " "), "api_key") {
		t.Fatalf("unselected field must not carry cursor: %q", apiKeyLine)
	}
}

func TestCredentialSetKeysWorkForOllama(t *testing.T) {
	a := credentialApp(t, "ollama")
	a.Update(runeKey('s'))
	if !a.credentialState.setMode {
		t.Fatalf("'s' must enter set mode for ollama")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEscape})
	a.Update(runeKey('e'))
	if !a.credentialState.envMode {
		t.Fatalf("'e' must enter env mode for ollama")
	}
}

func TestCredentialOllamaPortValidation(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	resolver, err := credentials.NewResolver(workdir)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	a := New(Options{Workdir: workdir, Resolver: resolver})
	a.view = viewCredentials
	a.credentialState.providers = []string{"ollama"}
	a.credentialState.backend = credentials.SourceUserFile

	// enter set mode for host (first field)
	a.Update(runeKey('l')) // move to port
	a.Update(runeKey('s'))
	a.editor.SetValue("abc")
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if !a.credentialState.setMode {
		t.Fatalf("invalid port should keep editor open")
	}
	if _, _, ok := resolver.Lookup("ollama", "port"); ok {
		t.Fatalf("invalid port must not be stored")
	}

	a.editor.SetValue("8080")
	m, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = m.(*App)
	if v, _, ok := resolver.Lookup("ollama", "port"); !ok || v != "8080" {
		t.Fatalf("valid port should be stored, got %q", v)
	}
}
