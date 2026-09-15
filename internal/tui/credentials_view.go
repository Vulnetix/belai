package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/credentials"
)

// viewState selects which full-screen view is active.
type viewState int

const (
	viewChat viewState = iota
	viewCredentials
	viewSettings
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
