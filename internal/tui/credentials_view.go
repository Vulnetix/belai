package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/credentials"
)

// credentialViewState tracks the credential manager UI.
type credentialViewState struct {
	providers   []string
	selectedIdx int
	setMode     bool
	backend     credentials.Source
	sets        map[string]credentials.Set
}

var credentialHeader = lipgloss.NewStyle().Bold(true).Underline(true)

func (a *App) credentialView() string {
	var b strings.Builder
	b.WriteString(credentialHeader.Render("Credential Manager") + "\n\n")

	sets := a.credentialState.sets
	if sets == nil {
		sets = map[string]credentials.Set{}
		for _, p := range a.credentialState.providers {
			if a.resolver != nil {
				sets[p] = a.resolver.Resolve(p)
			} else {
				sets[p] = credentials.Set{Provider: p, Missing: []string{"api_key"}}
			}
		}
		a.credentialState.sets = sets
	}

	for i, p := range a.credentialState.providers {
		set := sets[p]
		prefix := "  "
		if i == a.credentialState.selectedIdx {
			prefix = "> "
		}
		b.WriteString(prefix + p + "\n")
		for _, f := range credentials.Spec(p) {
			v, ok := set.Values[f.Name]
			status := "missing"
			from := "—"
			if ok {
				status = "configured"
				from = string(v.Source)
			}
			b.WriteString(fmt.Sprintf("    %-12s %-12s %s\n", f.Name, status, from))
		}
		if len(set.Notes) > 0 {
			for _, note := range set.Notes {
				b.WriteString("    note: " + note + "\n")
			}
		}
	}

	if a.resolver != nil {
		backends := a.resolver.Backends()
		var parts []string
		for _, be := range backends {
			p := be.Name
			if be.Writable {
				p += " writable"
			} else {
				p += " read-only"
			}
			if be.Available {
				p += " available"
			} else {
				if be.Reason != "" {
					p += " unavailable (" + be.Reason + ")"
				} else {
					p += " unavailable"
				}
			}
			parts = append(parts, p)
		}
		b.WriteString("\nbackends: " + strings.Join(parts, " · ") + "\n")
	}

	b.WriteString("\nkeys: s set · c clear · b cycle backend · esc back\n")
	return b.String()
}

// initCredentialState populates the credential view state.
func (a *App) initCredentialState() {
	a.credentialState = credentialViewState{
		providers: []string{"openai", "anthropic", "cloudflare-workers-ai", "cloudflare-ai-gateway"},
		backend:   credentials.SourceUserFile,
	}
	if a.resolver != nil {
		for _, be := range a.resolver.Backends() {
			if be.Name == "keychain" && be.Available {
				a.credentialState.backend = credentials.SourceKeychain
				break
			}
		}
	}
}

// handleCredentialKey is the key handler for the credential manager view.
func (a *App) handleCredentialKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.credentialState.setMode {
		switch m.String() {
		case "esc":
			a.credentialState.setMode = false
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			val := a.editor.Value()
			p := a.credentialState.providers[a.credentialState.selectedIdx]
			spec := credentials.Spec(p)
			if len(spec) > 0 && a.resolver != nil {
				_ = a.resolver.Store(p, spec[0].Name, val, a.credentialState.backend)
			}
			a.editor.Reset()
			a.editor.Masked = false
			a.credentialState.setMode = false
			a.refreshCredentials()
			return a, a.refreshProvider()
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "up", "k":
		if a.credentialState.selectedIdx > 0 {
			a.credentialState.selectedIdx--
		}
		return a, nil
	case "down", "j":
		if a.credentialState.selectedIdx < len(a.credentialState.providers)-1 {
			a.credentialState.selectedIdx++
		}
		return a, nil
	case "esc":
		a.pop()
		return a, nil
	case "s":
		a.credentialState.setMode = true
		a.editor.Masked = true
		_ = a.editor.Focus()
		return a, nil
	case "c":
		if a.resolver != nil {
			p := a.credentialState.providers[a.credentialState.selectedIdx]
			for _, f := range credentials.Spec(p) {
				_ = a.resolver.Clear(p, f.Name, a.credentialState.backend)
			}
			a.refreshCredentials()
			return a, a.refreshProvider()
		}
		return a, nil
	case "b":
		if a.resolver != nil {
			backends := a.resolver.Backends()
			var writable []credentials.Source
			for _, be := range backends {
				if be.Writable && be.Available {
					writable = append(writable, credentials.Source(be.Name))
				}
			}
			if len(writable) == 0 {
				return a, nil
			}
			for i, s := range writable {
				if s == a.credentialState.backend {
					a.credentialState.backend = writable[(i+1)%len(writable)]
					break
				}
			}
		}
		return a, nil
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

func (a *App) refreshCredentials() {
	a.credentialState.sets = nil
}
