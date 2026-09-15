package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agentscan"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
)

// importViewState tracks the credential import UI.
type importViewState struct {
	rows       []importRow
	selected   int
	backend    credentials.Source
	scope      config.Scope
	confirming bool
	errorMsg   string
	scanned    bool
}

// importRow is one discovered credential in the import list.
type importRow struct {
	found     agentscan.Found
	chosen    bool
	asEnvRef  bool
	envName   string
	existing  string
	overwrite bool
}

var importHeader = lipgloss.NewStyle().Bold(true).Underline(true)

func (a *App) enterImport() tea.Cmd {
	a.importState = importViewState{
		backend: a.credentialState.backend,
		scope:   config.ScopeGlobal,
		rows:    a.importRows(),
		scanned: true,
	}
	return nil
}

func (a *App) importRows() []importRow {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	found := agentscan.Scan(home)
	rows := make([]importRow, 0, len(found))
	for _, f := range found {
		row := importRow{found: f}
		if f.EnvKey != "" {
			row.envName = f.EnvKey
			row.asEnvRef = true
		} else if f.Profile != nil && f.Profile.APIKeyEnv != "" {
			row.envName = f.Profile.APIKeyEnv
			row.asEnvRef = true
		}
		if f.Importable() && a.resolver != nil {
			if _, origin, ok := a.resolver.Lookup(f.Provider, f.Field); ok {
				row.existing = origin
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) importView() string {
	var b strings.Builder
	b.WriteString(importHeader.Render("Import Credentials") + "\n\n")
	b.WriteString(fmt.Sprintf("backend: %s   scope: %s\n\n", string(a.importState.backend), string(a.importState.scope)))

	if len(a.importState.rows) == 0 {
		b.WriteString("Nothing found. Press r to rescan.\n")
	} else {
		for i, row := range a.importState.rows {
			prefix := "  "
			if i == a.importState.selected {
				prefix = "> "
			}
			mark := " "
			if row.chosen {
				mark = "x"
			}
			status := a.importRowStatus(row)
			line := fmt.Sprintf("%s[%s] %-10s %-22s %-12s %-12s %s", prefix, mark, row.found.Agent, row.found.Provider, row.found.Field, row.found.Mask(), status)
			if !row.found.Importable() {
				b.WriteString(dimStyle.Render(line) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
	}

	if a.importState.confirming {
		b.WriteString("\noverwrite existing credential? y/n\n")
	}
	if a.importState.errorMsg != "" {
		b.WriteString("\nerror: " + a.importState.errorMsg + "\n")
	}
	b.WriteString("\nkeys: space toggle · v env-ref · o overwrite · b backend · s scope · r rescan · enter save · esc back\n")
	return b.String()
}

func (a *App) importRowStatus(row importRow) string {
	if !row.found.Importable() {
		return row.found.Note
	}
	if row.overwrite {
		return "OVERWRITE"
	}
	if row.existing != "" {
		return "already set from " + row.existing
	}
	if row.asEnvRef {
		if row.envName != "" {
			return "env $" + row.envName
		}
		return "env reference"
	}
	return "new"
}

func (a *App) handleImportKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.importState.confirming {
		switch m.String() {
		case "y":
			a.importState.confirming = false
			for i := range a.importState.rows {
				if a.importState.rows[i].chosen && a.importState.rows[i].existing != "" {
					a.importState.rows[i].overwrite = true
				}
			}
			return a, a.doImport()
		case "n", "esc":
			a.importState.confirming = false
			return a, nil
		}
		return a, nil
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.importState.selected > 0 {
			a.importState.selected--
		}
		return a, nil
	case "down", "j":
		if a.importState.selected < len(a.importState.rows)-1 {
			a.importState.selected++
		}
		return a, nil
	case "r":
		a.importState.rows = a.importRows()
		a.importState.scanned = true
		a.importState.errorMsg = ""
		return a, nil
	case " ":
		if a.importState.selected < len(a.importState.rows) {
			row := &a.importState.rows[a.importState.selected]
			if row.found.Importable() {
				row.chosen = !row.chosen
			}
		}
		return a, nil
	case "v":
		if a.importState.selected < len(a.importState.rows) {
			row := &a.importState.rows[a.importState.selected]
			if row.found.Importable() {
				row.asEnvRef = !row.asEnvRef
				if row.asEnvRef && row.envName == "" {
					row.envName = importEnvVar(row.found.Provider)
				}
			}
		}
		return a, nil
	case "o":
		if a.importState.selected < len(a.importState.rows) {
			row := &a.importState.rows[a.importState.selected]
			if row.found.Importable() && row.existing != "" {
				row.overwrite = !row.overwrite
			}
		}
		return a, nil
	case "b":
		a.importState.backend = a.cycleWritableBackend(a.importState.backend)
		return a, nil
	case "s":
		if a.importState.scope == config.ScopeGlobal {
			a.importState.scope = config.ScopeProject
		} else {
			a.importState.scope = config.ScopeGlobal
		}
		return a, nil
	case "enter":
		for _, row := range a.importState.rows {
			if row.chosen && row.existing != "" && !row.overwrite {
				a.importState.confirming = true
				return a, nil
			}
		}
		return a, a.doImport()
	}
	return a, nil
}

func (a *App) doImport() tea.Cmd {
	if a.resolver == nil {
		a.importState.errorMsg = "no resolver configured"
		return nil
	}

	// 1. Profiles first, so a failing profile write aborts before any secret
	// is written.
	for _, row := range a.importState.rows {
		if !row.chosen || row.found.Profile == nil {
			continue
		}
		name := row.found.Provider
		prof := *row.found.Profile
		if len(row.found.Models) > 0 {
			prof.Models = row.found.Models
		}
		if err := config.Mutate(a.importState.scope, a.workdir, func(s *config.Settings) error {
			if s.Providers == nil {
				s.Providers = map[string]config.ProviderProfile{}
			}
			s.Providers[name] = prof
			for _, m := range row.found.Models {
				if m.ContextWindow > 0 {
					if s.ContextWindows == nil {
						s.ContextWindows = map[string]int{}
					}
					s.ContextWindows[m.ID] = m.ContextWindow
				}
			}
			return nil
		}); err != nil {
			a.importState.errorMsg = err.Error()
			return nil
		}
	}

	// 2. Credentials.
	for _, row := range a.importState.rows {
		if !row.chosen {
			continue
		}
		var err error
		if row.asEnvRef {
			if row.envName == "" {
				a.importState.errorMsg = "no env var name to reference"
				return nil
			}
			err = a.resolver.StoreEnvRef(row.found.Provider, row.found.Field, row.envName, a.importState.backend)
		} else {
			err = a.resolver.Store(row.found.Provider, row.found.Field, row.found.Reveal(), a.importState.backend)
		}
		if err != nil {
			a.importState.errorMsg = err.Error()
			return nil
		}
	}

	a.refreshCredentials()
	if err := a.reloadSettings(); err != nil {
		a.importState.errorMsg = err.Error()
		return nil
	}
	a.pop()
	return a.refreshProvider()
}

func (a *App) cycleWritableBackend(cur credentials.Source) credentials.Source {
	if a.resolver == nil {
		return cur
	}
	var writable []credentials.Source
	for _, be := range a.resolver.Backends() {
		if be.Writable && be.Available {
			writable = append(writable, credentials.Source(be.Name))
		}
	}
	if len(writable) == 0 {
		return cur
	}
	for i, s := range writable {
		if s == cur {
			return writable[(i+1)%len(writable)]
		}
	}
	return cur
}

func importEnvVar(provider string) string {
	var b strings.Builder
	b.WriteString("SIGNET_")
	for i := 0; i < len(provider); i++ {
		c := provider[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - 'a' + 'A')
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	b.WriteString("_API_KEY")
	return b.String()
}
