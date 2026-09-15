package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/tui/components"
)

// credentialViewState tracks the credential manager UI.
type credentialViewState struct {
	providers   []string
	selectedIdx int
	fieldIdx    int // selected field within the selected provider
	setMode     bool
	envMode     bool
	backend     credentials.Source
	sets        map[string]credentials.Set
}

func (a *App) credentialView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Credential Manager", "esc back", w))

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
		providerSelected := i == a.credentialState.selectedIdx
		name := p
		if providerSelected {
			name = components.EmphStyle.Render(p)
		} else {
			name = components.MutedStyle.Render(p)
		}
		b.WriteString(components.Cursor(providerSelected) + name + "\n")

		for j, f := range credentials.Spec(p) {
			v, ok := set.Values[f.Name]
			// Pad inside the style: padding a pre-styled string would count
			// the escape sequences as columns.
			status := components.MutedStyle.Width(16).Render("○ missing")
			from := components.MutedStyle.Render("—")
			if ok {
				status = components.AccentStyle.Width(16).Render("● configured")
				from = components.MutedStyle.Render(string(v.Source))
			}
			marker := "    "
			if providerSelected && j == a.credentialState.fieldIdx {
				marker = "  > "
			}
			b.WriteString(marker + fmt.Sprintf("%-14s ", f.Name) + status + " " + from + "\n")
		}
		for _, note := range set.Notes {
			b.WriteString("    " + components.MutedStyle.Render("│ "+note) + "\n")
		}
	}

	if a.resolver != nil {
		var parts []string
		for _, be := range a.resolver.Backends() {
			glyph, style := "●", components.AccentStyle
			if !be.Available {
				glyph, style = "○", components.MutedStyle
			}
			label := be.Name
			if be.Writable {
				label += " writable"
			} else {
				label += " read-only"
			}
			if !be.Available && be.Reason != "" {
				label += " (" + be.Reason + ")"
			}
			parts = append(parts, style.Render(glyph+" "+label))
		}
		b.WriteString("\n" + components.MutedStyle.Render("backends  ") +
			strings.Join(parts, components.MutedStyle.Render("  ·  ")) + "\n")
	}

	if a.credentialState.setMode || a.credentialState.envMode {
		title := "secret"
		if a.credentialState.envMode {
			title = "env var name"
		}
		b.WriteString("\n" + a.renderFieldEditor(title, w) + "\n")
	}

	b.WriteString("\n" + components.HelpBar(
		"s", "set", "e", "env ref", "c", "clear", "b", "backend",
		"i", "import", "h/l", "field", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// credentialFieldCount returns the number of settable fields on the currently
// selected provider. Providers without a spec (e.g. ollama) have none.
func (a *App) credentialFieldCount() int {
	if a.credentialState.selectedIdx >= len(a.credentialState.providers) {
		return 0
	}
	return len(credentials.Spec(a.credentialState.providers[a.credentialState.selectedIdx]))
}

// initCredentialState populates the credential view state.
func (a *App) initCredentialState() {
	a.credentialState = credentialViewState{
		providers: a.providerNames(),
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
	if a.credentialState.setMode || a.credentialState.envMode {
		switch m.String() {
		case "esc":
			a.credentialState.setMode = false
			a.credentialState.envMode = false
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			val := strings.TrimSpace(a.editor.Value())
			p := a.credentialState.providers[a.credentialState.selectedIdx]
			spec := credentials.Spec(p)
			if a.credentialState.fieldIdx < 0 || a.credentialState.fieldIdx >= len(spec) {
				a.credentialState.fieldIdx = 0
			}
			if len(spec) > 0 && a.resolver != nil {
				f := spec[a.credentialState.fieldIdx]
				if a.credentialState.envMode {
					_ = a.resolver.StoreEnvRef(p, f.Name, val, a.credentialState.backend)
				} else {
					_ = a.resolver.Store(p, f.Name, val, a.credentialState.backend)
				}
			}
			a.editor.Reset()
			a.editor.Masked = false
			a.credentialState.setMode = false
			a.credentialState.envMode = false
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
			a.credentialState.fieldIdx = 0
		}
		return a, nil
	case "down", "j":
		if a.credentialState.selectedIdx < len(a.credentialState.providers)-1 {
			a.credentialState.selectedIdx++
			a.credentialState.fieldIdx = 0
		}
		return a, nil
	case "left", "h":
		if a.credentialState.fieldIdx > 0 {
			a.credentialState.fieldIdx--
		}
		return a, nil
	case "right", "l":
		if n := a.credentialFieldCount(); n > 1 && a.credentialState.fieldIdx < n-1 {
			a.credentialState.fieldIdx++
		}
		return a, nil
	case "esc":
		a.pop()
		return a, nil
	case "s":
		if a.credentialFieldCount() == 0 {
			return a, nil
		}
		a.credentialState.setMode = true
		a.editor.Masked = true
		_ = a.editor.Focus()
		return a, nil
	case "e":
		if a.credentialFieldCount() == 0 {
			return a, nil
		}
		a.credentialState.envMode = true
		a.editor.Masked = false
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
	case "i":
		return a, a.push(viewImport)
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

func (a *App) refreshCredentials() {
	a.credentialState.sets = nil
}
