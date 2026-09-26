package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// The Kiro sign-in on the provider detail's credentials tab: the same device
// grant as `belai login kiro`, with the code shown in the view. The device
// code, client secret and tokens never reach the view, a transcript or a log;
// the finished login goes straight to the chosen credential backend.

type (
	kiroGrantMsg struct {
		grant kiroauth.Grant
		login kiroauth.DeviceLogin
		err   error
	}
	kiroLoginDoneMsg struct {
		login kiroauth.Login
		err   error
	}
)

// kiroLoginState is the sign-in's progress.
type kiroLoginState struct {
	urlMode bool // the editor holds "[start-url [region]]"
	busy    bool
	grant   *kiroauth.Grant
	status  string
	cancel  context.CancelFunc
}

const kiroEditorTitle = "start URL [region] · enter alone signs in with an AWS Builder ID"

// kiroBeginSignIn opens the editor for the optional Identity Center start URL.
func (a *App) kiroBeginSignIn() (tea.Model, tea.Cmd) {
	st := &a.kiroLogin
	if st.busy {
		return a, nil
	}
	st.urlMode, st.status, st.grant = true, "", nil
	a.editor.Reset()
	a.editor.Masked = false
	_ = a.editor.Focus()
	return a, nil
}

// kiroStartSignIn parses the editor and starts the device grant.
func (a *App) kiroStartSignIn() (tea.Model, tea.Cmd) {
	st := &a.kiroLogin
	fields := strings.Fields(a.editor.Value())
	a.editor.Reset()
	st.urlMode = false
	var d kiroauth.DeviceLogin
	if len(fields) > 0 {
		d.StartURL = fields[0]
	}
	if len(fields) > 1 {
		d.Region = fields[1]
	}
	if len(fields) > 2 {
		st.status = "✗ enter a start URL and, optionally, its region"
		return a, nil
	}
	st.busy, st.status = true, "starting the AWS sign-in…"
	ctx, cancel := context.WithCancel(a.baseCtx())
	st.cancel = cancel
	return a, func() tea.Msg {
		g, err := d.Start(ctx)
		return kiroGrantMsg{grant: g, login: d, err: err}
	}
}

// kiroCancelSignIn stops a running sign-in.
func (a *App) kiroCancelSignIn() {
	if a.kiroLogin.cancel != nil {
		a.kiroLogin.cancel()
		a.kiroLogin.cancel = nil
	}
}

// handleKiroLoginMsg applies the sign-in's async results.
func (a *App) handleKiroLoginMsg(msg tea.Msg) (tea.Cmd, bool) {
	st := &a.kiroLogin
	switch m := msg.(type) {
	case kiroGrantMsg:
		if m.err != nil {
			st.busy, st.status = false, "✗ "+m.err.Error()
			return nil, true
		}
		st.grant = &m.grant
		st.status = "waiting for the sign-in to be approved…"
		_ = vulnetixcli.OpenBrowser(m.grant.BrowseURL())
		ctx, cancel := context.WithCancel(a.baseCtx())
		a.kiroCancelSignIn()
		st.cancel = cancel
		d, g := m.login, m.grant
		return func() tea.Msg {
			l, err := d.Poll(ctx, g)
			return kiroLoginDoneMsg{login: l, err: err}
		}, true
	case kiroLoginDoneMsg:
		st.busy, st.grant = false, nil
		if m.err != nil {
			if errors.Is(m.err, context.Canceled) {
				st.status = "sign-in cancelled"
			} else {
				st.status = "✗ " + m.err.Error()
			}
			return nil, true
		}
		if a.resolver == nil {
			st.status = "✗ no credential store to save the sign-in to"
			return nil, true
		}
		backend := a.providerDetailState.backend
		if backend != credentials.SourceKeychain && backend != credentials.SourceUserFile {
			backend = a.resolver.PreferredBackend()
		}
		if err := a.resolver.Store("kiro", "login", m.login.Encode(), backend); err != nil {
			st.status = "✗ could not store the sign-in: " + err.Error()
			return nil, true
		}
		st.status = "● signed in · stored in the " + string(backend)
		a.refreshProviderDetail()
		return a.refreshProvider(), true
	}
	return nil, false
}

// kiroSignInView renders the sign-in block under the credential fields.
func (a *App) kiroSignInView(w int) string {
	st := &a.kiroLogin
	var b strings.Builder
	if st.urlMode {
		b.WriteString("\n" + a.renderFieldEditor(kiroEditorTitle, w) + "\n")
	}
	if st.grant != nil {
		b.WriteString("\n    open  " + components.AccentStyle.Render(st.grant.BrowseURL()) + "\n")
		b.WriteString("    code  " + components.EmphStyle.Render(st.grant.UserCode) + "\n")
	}
	if st.status != "" {
		b.WriteString("\n    " + components.MutedStyle.Render(st.status) + "\n")
	}
	return b.String()
}
