package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/credentials"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tui/components"
)

// providersViewState tracks the /providers master list.
type providersViewState struct {
	filter        string
	filtering     bool
	cursor        int
	scroll        int
	rows          []providerRow
	report        string
	reportPending bool
}

// providerRow is one rendered line in the master list.
type providerRow struct {
	isHeader bool
	addNew   bool   // the synthetic "+ add new provider" entry
	name     string // provider name or group header
	label    string // optional extra label for headers
}

// providerDetailViewState tracks the provider drill-down.
type providerDetailViewState struct {
	provider           string
	tab                int // 0=credentials, 1=models, 2=server
	fieldIdx           int
	scroll             int
	modelCursor        int
	modelFilter        string
	modelFiltering     bool
	modelScroll        int
	setMode            bool
	envMode            bool
	repoMode           bool
	repoAction         repoAction
	backend            credentials.Source
	sets               map[string]credentials.Set
	localReport        string
	localReportPending bool
	// endpointFields drives the server-tab endpoint editor for a custom
	// kind'd provider (protocol/host/port/display name).
	endpointFields   []providerNewField
	endpointFieldSel int
	endpointEditing  bool
}

// repoAction selects which local-model command the server-tab editor commits.
type repoAction int

const (
	repoActionNone repoAction = iota
	repoActionLaunch
	repoActionDownload
)

const (
	providerTabCredentials = iota
	providerTabModels
	providerTabServer
)

var providerTabNames = []string{"credentials", "models", "server"}

// enterProviders seeds the master list and refreshes availability/catalogues.
func (a *App) enterProviders() tea.Cmd {
	a.providersState = providersViewState{}
	a.rebuildProviderRows()
	return tea.Batch(a.availabilityCmdIfStale(), a.prefetchAllCatalogsCmd())
}

// prefetchAllCatalogsCmd warms catalogues for every configured provider so the
// master list can show accurate model counts.
func (a *App) prefetchAllCatalogsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, name := range a.providerNames() {
		cmds = append(cmds, a.prefetchCatalogCmd(name))
	}
	return tea.Batch(cmds...)
}

// rebuildProviderRows recomputes the grouped master list from the current
// filter. Configured providers are listed first, then unconfigured.
func (a *App) rebuildProviderRows() {
	all := a.providerNames()
	configuredSet := map[string]bool{}
	if a.resolver != nil {
		for _, name := range a.resolver.ConfiguredProviders() {
			configuredSet[name] = true
		}
	}

	var configured, notConfigured []providerRow
	for _, name := range all {
		if !rowMatchesFilter(name, a.providersState.filter) {
			continue
		}
		if configuredSet[name] {
			configured = append(configured, providerRow{name: name})
		} else {
			notConfigured = append(notConfigured, providerRow{name: name})
		}
	}

	rows := make([]providerRow, 0, len(configured)+len(notConfigured)+3)
	rows = append(rows, providerRow{addNew: true})
	if len(configured) > 0 {
		rows = append(rows, providerRow{isHeader: true, name: "configured"})
		rows = append(rows, configured...)
	}
	if len(notConfigured) > 0 {
		rows = append(rows, providerRow{isHeader: true, name: "not configured"})
		rows = append(rows, notConfigured...)
	}
	a.providersState.rows = rows
	a.providersState.cursor = clampIdx(a.providersState.cursor, a.providersRowCount())
}

func rowMatchesFilter(name, filter string) bool {
	if filter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(filter))
}

func (a *App) providersRowCount() int {
	c := 0
	for _, r := range a.providersState.rows {
		if !r.isHeader {
			c++
		}
	}
	return c
}

func (a *App) selectedProviderRow() (int, providerRow) {
	idx := 0
	for i, r := range a.providersState.rows {
		if r.isHeader {
			continue
		}
		if idx == a.providersState.cursor {
			return i, r
		}
		idx++
	}
	return -1, providerRow{}
}

func (a *App) providersView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Providers", "esc back", w))

	counter := fmt.Sprintf("%d/%d", a.providersState.cursor+1, max(1, a.providersRowCount()))
	if a.providersState.filter != "" {
		counter += fmt.Sprintf(" (of %d)", len(a.providerNames()))
	}
	b.WriteString(components.MutedStyle.Render(counter) + "\n\n")

	// Pre-list search line.
	headLines := 3
	tailLines := 3
	if a.providersState.filtering {
		b.WriteString(components.AccentStyle.Render("search  "+a.providersState.filter+"▌") + "\n")
	} else if a.providersState.filter != "" {
		b.WriteString(components.MutedStyle.Render("search  ") +
			components.EmphStyle.Render(a.providersState.filter) +
			components.MutedStyle.Render("  esc clears") + "\n")
	} else {
		b.WriteString(components.MutedStyle.Render("search  / to filter") + "\n")
		headLines = 3
	}

	rows := a.providersState.rows
	cursorAbs, _ := a.selectedProviderRow()
	listRows := a.height - headLines - tailLines - 2 // padding
	if listRows < 3 {
		listRows = 3
	}

	start := a.providersState.scroll
	if start > cursorAbs-listRows && cursorAbs >= 0 {
		start = cursorAbs - listRows + 1
	}
	if start < 0 {
		start = 0
	}
	if start > len(rows)-listRows {
		start = max(0, len(rows)-listRows)
	}
	a.providersState.scroll = start
	end := min(start+listRows, len(rows))

	for i := start; i < end; i++ {
		r := rows[i]
		if r.isHeader {
			b.WriteString("\n" + components.MutedStyle.Render(strings.ToUpper(r.name)) + "\n")
			continue
		}
		selected := i == cursorAbs
		var line string
		if r.addNew {
			line = components.Cursor(selected) + components.AccentStyle.Render("+ add new provider")
			if selected {
				line = components.Cursor(true) + components.AccentStyle.Bold(true).Render("+ add new provider")
			}
		} else {
			line = a.renderProviderRow(r.name, selected)
		}
		b.WriteString(line + "\n")
	}

	var meta []string
	if a.providersState.scroll > 0 {
		meta = append(meta, fmt.Sprintf("↑ %d more", a.providersState.scroll))
	}
	if end < len(rows) {
		meta = append(meta, fmt.Sprintf("↓ %d more", len(rows)-end))
	}
	if len(meta) > 0 {
		b.WriteString(components.MutedStyle.Render("  "+strings.Join(meta, "  ·  ")) + "\n")
	}
	if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("  no providers match the filter") + "\n")
	}

	if a.avail.note != "" {
		b.WriteString("\n" + components.MutedStyle.Render(a.avail.note) + "\n")
	}

	if a.providersState.reportPending {
		b.WriteString("\n" + components.AccentStyle.Render("○ probing…") + "\n")
	} else if a.providersState.report != "" {
		b.WriteString("\n" + a.providersState.report + "\n")
	}

	if a.providersState.filtering {
		b.WriteString("\n" + components.HelpBar("type", "filter", "enter", "accept", "esc", "clear") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "provider", "/", "filter", "enter", "open", "n", "new",
			"p", "local report", "i", "import", "r", "refetch", "esc", "back") + "\n")
	}

	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) renderProviderRow(name string, selected bool) string {
	var glyph, status string
	cfg, _ := run.Prepare("", name, credentialSourceOf(a.resolver))
	d, ok := provider.Lookup(name)
	if ok {
		switch {
		case d.Local:
			if a.providerAvailable(name) {
				glyph = "⏻"
				status = "running"
			} else {
				glyph = "○"
				status = "local"
			}
		case a.providerAvailable(name):
			glyph = "●"
			status = "bearer"
		default:
			glyph = "○"
			if d.Auth == provider.AuthXAPIKey {
				status = "x-api-key"
			} else if d.Auth == provider.AuthCFAIG {
				status = "cf-aig"
			} else if d.Auth == provider.AuthCopilot {
				status = "copilot"
			} else {
				status = "bearer"
			}
		}
	} else {
		if a.providerAvailable(name) {
			glyph = "●"
		} else {
			glyph = "○"
		}
		status = "custom"
		if p, ok := a.settings.Providers[name]; ok && p.Kind != "" && p.Kind != "openai-compatible" {
			status = p.Kind
		}
	}

	var extra string
	if catalog := a.catalogFor(name); len(catalog) > 0 {
		extra = fmt.Sprintf("%d models", len(catalog))
	} else if a.catalogLoading[name] {
		extra = "live"
	} else if a.catalogErr[name] != "" {
		extra = "static"
	} else if d.ListPath != "" {
		extra = "live"
	}
	if cfg.BaseURL != "" && d.Local {
		extra = cfg.BaseURL
	}

	// The display label (or host:port fallback) is the primary token; the
	// slug stays muted beside it so the underlying identity is never hidden.
	display := a.providerDisplayLabel(name)
	nameStr := display
	if selected {
		nameStr = components.EmphStyle.Render(display)
	} else {
		nameStr = components.MutedStyle.Render(display)
	}
	line := fmt.Sprintf("%s %-16s %-12s %s", glyph, nameStr, components.MutedStyle.Render(status), components.MutedStyle.Render(extra))
	if display != name {
		line += components.MutedStyle.Render("  (" + name + ")")
	}
	return components.Cursor(selected) + line
}

func (a *App) handleProvidersKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.providersState.filtering {
		switch m.Type {
		case tea.KeyEsc:
			a.providersState.filtering = false
			a.providersState.filter = ""
			a.rebuildProviderRows()
			return a, nil
		case tea.KeyEnter:
			a.providersState.filtering = false
			return a, nil
		case tea.KeyRunes:
			a.providersState.filter += string(m.Runes)
			a.providersState.cursor = 0
			a.rebuildProviderRows()
			return a, nil
		case tea.KeySpace:
			a.providersState.filter += " "
			a.providersState.cursor = 0
			a.rebuildProviderRows()
			return a, nil
		case tea.KeyBackspace:
			a.providersState.filter = trimLastRune(a.providersState.filter)
			a.providersState.cursor = 0
			a.rebuildProviderRows()
			return a, nil
		}
		return a, nil
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "/":
		a.providersState.filtering = true
		return a, nil
	case "up", "k":
		n := a.providersRowCount()
		if n > 0 {
			a.providersState.cursor = (a.providersState.cursor - 1 + n) % n
		}
		return a, nil
	case "down", "j":
		n := a.providersRowCount()
		if n > 0 {
			a.providersState.cursor = (a.providersState.cursor + 1) % n
		}
		return a, nil
	case "enter":
		_, r := a.selectedProviderRow()
		if r.addNew {
			a.openProviderNew()
			return a, a.push(viewProviderNew)
		}
		if r.name == "" {
			return a, nil
		}
		a.openProviderDetail(r.name)
		return a, a.push(viewProviderDetail)
	case "n":
		a.openProviderNew()
		return a, a.push(viewProviderNew)
	case "p":
		a.providersState.reportPending = true
		return a, a.localModelReportCmd("")
	case "i":
		return a, a.push(viewImport)
	case "r":
		return a, tea.Batch(a.availabilityCmdIfStale(), a.prefetchAllCatalogsCmd())
	}
	return a, nil
}

func (a *App) openProviderDetail(name string) {
	a.providerDetailState = providerDetailViewState{
		provider: name,
		tab:      providerTabCredentials,
		backend:  credentials.SourceUserFile,
	}
	if a.resolver != nil {
		a.providerDetailState.sets = map[string]credentials.Set{
			name: a.resolver.Resolve(name),
		}
	}
	a.setProviderDetailBackendDefault()
}

func (a *App) setProviderDetailBackendDefault() {
	if a.resolver == nil {
		return
	}
	for _, be := range a.resolver.Backends() {
		if be.Name == "keychain" && be.Available && be.Writable {
			a.providerDetailState.backend = credentials.SourceKeychain
			return
		}
	}
}

func (a *App) providerDetailView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader(a.providerDetailState.provider, "esc back", w))
	b.WriteString(a.providerDetailTabs(w) + "\n\n")

	switch a.providerDetailState.tab {
	case providerTabCredentials:
		b.WriteString(a.providerDetailCredentials(w))
	case providerTabModels:
		b.WriteString(a.providerDetailModels(w))
	case providerTabServer:
		b.WriteString(a.providerDetailServer(w))
	}

	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) providerDetailTabs(w int) string {
	var parts []string
	for i, t := range providerTabNames {
		// Hide "server" tab for non-local providers unless there are managed servers.
		if i == providerTabServer {
			if !a.isProviderTabServerVisible() {
				continue
			}
		}
		label := " " + t + " "
		if i == a.providerDetailState.tab {
			label = "[" + t + "]"
		}
		label = ansi.Truncate(label, w/3, "")
		if i == a.providerDetailState.tab {
			parts = append(parts, components.AccentStyle.Render(label))
		} else {
			parts = append(parts, components.MutedStyle.Render(label))
		}
	}
	return strings.Join(parts, "  ")
}

func (a *App) isProviderTabServerVisible() bool {
	if a.isProviderKindLocal(a.providerDetailState.provider) {
		return true
	}
	for _, s := range a.runningLocalServers() {
		if s.baseURL != "" {
			return true
		}
	}
	return false
}

// isProviderKindLocal reports whether a provider's availability is a running
// local server: a built-in with Descriptor.Local or a custom profile whose
// kind template is local.
func (a *App) isProviderKindLocal(name string) bool {
	if d, ok := provider.Lookup(name); ok {
		return d.Local
	}
	p, ok := a.settings.Providers[name]
	if !ok {
		return false
	}
	d, ok := provider.Template(p.Kind)
	return ok && d.Local
}

func (a *App) providerDetailCredentials(w int) string {
	var b strings.Builder
	name := a.providerDetailState.provider
	set := credentials.Set{Provider: name, Missing: []string{"api_key"}}
	if a.providerDetailState.sets != nil {
		if s, ok := a.providerDetailState.sets[name]; ok {
			set = s
		}
	}

	for j, f := range credentials.Spec(name) {
		v, ok := set.Values[f.Name]
		status := components.MutedStyle.Width(16).Render("○ missing")
		from := components.MutedStyle.Render("—")
		if ok {
			status = components.AccentStyle.Width(16).Render("● configured")
			from = components.MutedStyle.Render(string(v.Source))
		} else if f.Optional {
			status = components.MutedStyle.Width(16).Render("○ optional")
		}
		marker := "    "
		if j == a.providerDetailState.fieldIdx {
			marker = components.Cursor(true) + " "
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s %s\n", marker, f.Name, status, from))
	}
	for _, note := range set.Notes {
		b.WriteString("    " + components.MutedStyle.Render("│ "+note) + "\n")
	}

	if a.providerDetailState.setMode || a.providerDetailState.envMode {
		title := "secret"
		if a.providerDetailState.envMode {
			title = "env var name"
		}
		b.WriteString("\n" + a.renderFieldEditor(title, w) + "\n")
	}

	if a.resolver != nil {
		b.WriteString("\nstoring to  " +
			components.Chip(string(a.providerDetailState.backend), components.ColorTealSoft) + "\n")
		var backendParts []string
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
			backendParts = append(backendParts, style.Render(glyph+" "+label))
		}
		b.WriteString(components.MutedStyle.Render("backends  ") +
			strings.Join(backendParts, components.MutedStyle.Render("  ·  ")) + "\n")
	}

	b.WriteString("\n" + components.HelpBar(
		"←→", "tab", "↑↓", "field", "s", "set", "e", "env ref",
		"c", "clear", "b", "backend", "i", "import", "esc", "back") + "\n")
	return b.String()
}

func (a *App) providerDetailCredentialFieldCount() int {
	return len(credentials.Spec(a.providerDetailState.provider))
}

func (a *App) providerDetailModels(w int) string {
	name := a.providerDetailState.provider
	catalog := a.catalogFor(name)
	filtered := filterModels(catalog, a.providerDetailState.modelFilter)

	var b strings.Builder
	if a.providerDetailState.modelFiltering {
		b.WriteString(components.AccentStyle.Render("search  "+a.providerDetailState.modelFilter+"▌") + "\n")
	} else if a.providerDetailState.modelFilter != "" {
		b.WriteString(components.MutedStyle.Render("search  ") +
			components.EmphStyle.Render(a.providerDetailState.modelFilter) +
			components.MutedStyle.Render("  esc clears") + "\n")
	} else {
		b.WriteString(components.MutedStyle.Render("search  / to filter") + "\n")
	}

	if a.catalogLoading[name] {
		if url := a.catalogURLs[name]; url != "" {
			b.WriteString(components.AccentStyle.Render("  ○ Fetching models from GET "+url+"…") + "\n")
		} else {
			b.WriteString(components.AccentStyle.Render("  ○ Fetching models…") + "\n")
		}
	} else if len(filtered) == 0 {
		b.WriteString(components.MutedStyle.Render("  no models — type or import a model id") + "\n")
	} else {
		const rows = 10
		cursor := clampIdx(a.providerDetailState.modelCursor, len(filtered))
		start := windowStart(a.providerDetailState.modelScroll, cursor, len(filtered), rows)
		a.providerDetailState.modelScroll = start
		end := min(start+rows, len(filtered))
		for i := start; i < end; i++ {
			m := filtered[i]
			selected := i == cursor
			line := m.ID
			if selected {
				line = components.EmphStyle.Render(line)
			}
			if m.ID == a.cfg.Model && name == a.cfg.Provider {
				line += components.AccentStyle.Render("  ● agent")
			}
			if cls := a.settings.Classifier; cls != nil && m.ID == cls.Model && name == cls.Provider {
				line += components.AccentStyle.Render("  ● classifier")
			}
			b.WriteString(components.Cursor(selected) + line + "\n")
		}
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  %d/%d", cursor+1, len(filtered))) + "\n")
	}

	if err := a.catalogErr[name]; err != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ fetch: "+err) + "\n")
	}

	if a.providerDetailState.modelFiltering {
		b.WriteString("\n" + components.HelpBar("type", "filter", "enter", "accept", "esc", "clear") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "model", "/", "filter", "r", "refetch",
			"enter", "set agent", "g", "set classifier", "esc", "back") + "\n")
	}
	return b.String()
}

func (a *App) providerDetailServer(w int) string {
	var b strings.Builder
	if a.providerDetailState.repoMode {
		b.WriteString(a.renderFieldEditor("repo [--port N] [--quant Q]", w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "commit", "esc", "cancel") + "\n")
		return b.String()
	}
	if a.providerDetailEndpointEditing() {
		return a.providerDetailEndpointView(w)
	}
	if a.providerDetailState.localReportPending {
		b.WriteString(components.AccentStyle.Render("○ probing…") + "\n")
	} else if a.providerDetailState.localReport != "" {
		b.WriteString(a.providerDetailState.localReport + "\n")
	} else {
		b.WriteString(components.MutedStyle.Render("press p to probe the local server status") + "\n")
	}
	b.WriteString("\n" + components.HelpBar(
		"d", "download", "l", "launch", "x", "stop", "p", "probe", "esc", "back") + "\n")
	return b.String()
}

// providerDetailEndpointEditing reports whether the current detail provider is
// a custom (non-built-in) profile, whose server tab shows the endpoint editor.
func (a *App) providerDetailEndpointEditing() bool {
	name := a.providerDetailState.provider
	if provider.Builtin(name) {
		return false
	}
	_, ok := a.settings.Providers[name]
	return ok
}

// ensureEndpointFields builds the endpoint editor rows from the live profile.
func (a *App) ensureEndpointFields() {
	name := a.providerDetailState.provider
	p, ok := a.settings.Providers[name]
	if !ok {
		a.providerDetailState.endpointFields = nil
		return
	}
	protocol := p.Protocol
	if protocol == "" {
		protocol = "http"
	}
	display := a.settings.LabelFor(name)
	if display == name {
		display = p.Host
		if p.Port != "" {
			display = p.Host + ":" + p.Port
		}
	}
	a.providerDetailState.endpointFields = []providerNewField{
		{key: "protocol", label: "protocol", kind: "cycle", opts: []string{"http", "https"}, value: protocol},
		{key: "host", label: "host", kind: "text", value: p.Host},
		{key: "port", label: "port", kind: "text", value: p.Port},
		{key: "display", label: "display name", kind: "text", value: display},
	}
	if a.providerDetailState.endpointFieldSel >= len(a.providerDetailState.endpointFields) {
		a.providerDetailState.endpointFieldSel = 0
	}
}

func (a *App) providerDetailEndpointView(w int) string {
	if len(a.providerDetailState.endpointFields) == 0 {
		a.ensureEndpointFields()
	}
	var b strings.Builder
	b.WriteString(components.MutedStyle.Render("endpoint of this provider instance") + "\n\n")

	if a.providerDetailState.endpointEditing {
		f := a.providerDetailState.endpointFields[a.providerDetailState.endpointFieldSel]
		b.WriteString(a.renderFieldEditor(f.label, w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
		return b.String()
	}

	for i, f := range a.providerDetailState.endpointFields {
		selected := i == a.providerDetailState.endpointFieldSel
		label := fmt.Sprintf("%-14s", f.label)
		value := f.value
		labelOut := components.MutedStyle.Render(label)
		valueOut := value
		if selected {
			labelOut = components.AccentStyle.Bold(true).Render(label)
			valueOut = components.EmphStyle.Render(value)
		} else if f.kind == "cycle" {
			valueOut = components.MutedStyle.Render(value)
		}
		b.WriteString(components.Cursor(selected) + labelOut + "  " + valueOut + "\n")
	}
	if a.providerDetailState.localReport != "" {
		b.WriteString("\n" + components.DangerStyle.Render(a.providerDetailState.localReport) + "\n")
	}
	b.WriteString("\n" + components.HelpBar(
		"↑↓", "move", "enter/space", "edit·cycle", "←/→", "cycle", "c", "save", "esc", "back") + "\n")
	return b.String()
}

func (a *App) handleProviderDetailKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.providerDetailState.setMode || a.providerDetailState.envMode || a.providerDetailState.repoMode || a.providerDetailState.endpointEditing {
		switch m.String() {
		case "esc":
			a.providerDetailState.setMode = false
			a.providerDetailState.envMode = false
			a.providerDetailState.repoMode = false
			a.providerDetailState.endpointEditing = false
			a.providerDetailState.repoAction = repoActionNone
			a.editor.Masked = false
			a.editor.Reset()
			return a, nil
		case "enter":
			if a.providerDetailState.endpointEditing {
				return a.providerDetailEndpointCommitField()
			}
			return a.providerDetailCommitField()
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	if a.providerDetailState.modelFiltering {
		switch m.Type {
		case tea.KeyEsc:
			a.providerDetailState.modelFiltering = false
			a.providerDetailState.modelFilter = ""
			a.providerDetailState.modelCursor = 0
			a.providerDetailState.modelScroll = 0
			return a, nil
		case tea.KeyEnter:
			a.providerDetailState.modelFiltering = false
			return a, nil
		case tea.KeyRunes:
			a.providerDetailState.modelFilter += string(m.Runes)
			a.providerDetailState.modelCursor = 0
			a.providerDetailState.modelScroll = 0
			return a, nil
		case tea.KeySpace:
			a.providerDetailState.modelFilter += " "
			a.providerDetailState.modelCursor = 0
			a.providerDetailState.modelScroll = 0
			return a, nil
		case tea.KeyBackspace:
			a.providerDetailState.modelFilter = trimLastRune(a.providerDetailState.modelFilter)
			a.providerDetailState.modelCursor = 0
			a.providerDetailState.modelScroll = 0
			return a, nil
		}
		return a, nil
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "left", "h":
		a.providerDetailCycleTab(-1)
		return a, nil
	case "right":
		a.providerDetailCycleTab(1)
		return a, nil
	case "l":
		if a.providerDetailState.tab != providerTabServer {
			a.providerDetailCycleTab(1)
			return a, nil
		}
	}

	switch a.providerDetailState.tab {
	case providerTabCredentials:
		return a.handleProviderDetailCredentialsKey(m)
	case providerTabModels:
		return a.handleProviderDetailModelsKey(m)
	case providerTabServer:
		return a.handleProviderDetailServerKey(m)
	}
	return a, nil
}

func (a *App) providerDetailCycleTab(dir int) {
	tabs := a.visibleProviderTabs()
	if len(tabs) == 0 {
		return
	}
	idx := indexOfInt(a.providerDetailState.tab, tabs)
	if idx < 0 {
		idx = 0
	}
	idx = (idx + dir + len(tabs)) % len(tabs)
	a.providerDetailState.tab = tabs[idx]
	a.providerDetailState.fieldIdx = 0
}

func (a *App) visibleProviderTabs() []int {
	var tabs []int
	for i := range providerTabNames {
		if i == providerTabServer && !a.isProviderTabServerVisible() {
			continue
		}
		tabs = append(tabs, i)
	}
	return tabs
}

func indexOfInt(v int, list []int) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func (a *App) handleProviderDetailCredentialsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.String() {
	case "up", "k":
		if a.providerDetailState.fieldIdx > 0 {
			a.providerDetailState.fieldIdx--
		}
		return a, nil
	case "down", "j":
		if n := a.providerDetailCredentialFieldCount(); n > 0 && a.providerDetailState.fieldIdx < n-1 {
			a.providerDetailState.fieldIdx++
		}
		return a, nil
	case "s":
		if a.providerDetailCredentialFieldCount() == 0 {
			return a, nil
		}
		a.providerDetailState.setMode = true
		a.editor.Masked = true
		_ = a.editor.Focus()
		return a, nil
	case "e":
		if a.providerDetailCredentialFieldCount() == 0 {
			return a, nil
		}
		a.providerDetailState.envMode = true
		a.editor.Masked = false
		_ = a.editor.Focus()
		return a, nil
	case "c":
		return a.providerDetailClearField()
	case "b":
		return a.providerDetailCycleBackend()
	case "i":
		return a, a.push(viewImport)
	}
	return a, nil
}

func (a *App) providerDetailCommitField() (tea.Model, tea.Cmd) {
	if a.providerDetailState.repoMode {
		return a.providerDetailCommitRepo()
	}

	val := strings.TrimSpace(a.editor.Value())
	p := a.providerDetailState.provider
	spec := credentials.Spec(p)
	if a.providerDetailState.fieldIdx < 0 || a.providerDetailState.fieldIdx >= len(spec) {
		a.providerDetailState.fieldIdx = 0
	}
	if len(spec) == 0 || a.resolver == nil {
		a.providerDetailState.setMode = false
		a.providerDetailState.envMode = false
		a.editor.Reset()
		a.editor.Masked = false
		return a, nil
	}
	f := spec[a.providerDetailState.fieldIdx]
	if p == "ollama" || p == "llama-server" {
		if f.Name == "port" && !config.ValidOllamaPort(val) {
			a.providerDetailState.setMode = false
			a.providerDetailState.envMode = false
			a.editor.Reset()
			a.editor.Masked = false
			return a, nil
		}
		if f.Name == "protocol" && !config.ValidOllamaProtocol(val) {
			a.providerDetailState.setMode = false
			a.providerDetailState.envMode = false
			a.editor.Reset()
			a.editor.Masked = false
			return a, nil
		}
	}
	if a.providerDetailState.envMode {
		_ = a.resolver.StoreEnvRef(p, f.Name, val, a.providerDetailState.backend)
	} else {
		_ = a.resolver.Store(p, f.Name, val, a.providerDetailState.backend)
	}
	a.editor.Reset()
	a.editor.Masked = false
	a.providerDetailState.setMode = false
	a.providerDetailState.envMode = false
	a.refreshProviderDetail()
	return a, a.refreshProvider()
}

// providerDetailCommitRepo parses the shared inline editor's value as a
// local-model repo line and dispatches launch or download.
func (a *App) providerDetailCommitRepo() (tea.Model, tea.Cmd) {
	val := strings.TrimSpace(a.editor.Value())
	a.editor.Reset()
	a.editor.Masked = false
	action := a.providerDetailState.repoAction
	a.providerDetailState.repoMode = false
	a.providerDetailState.repoAction = repoActionNone

	prefix := "launch"
	if action == repoActionDownload {
		prefix = "download"
	}
	_, flags := parseLocalModelArgs(prefix + " " + val)
	if flags.repo == "" {
		a.providerDetailState.localReport = "usage: /providers " + prefix + " <repo> [--port N] [--quant Q]"
		return a, nil
	}
	switch action {
	case repoActionLaunch:
		return a, a.localModelLaunchCmd(flags.repo, flags.port, flags.quant)
	case repoActionDownload:
		return a, a.localModelDownloadCmd(flags.repo, flags.quant)
	}
	return a, nil
}

func (a *App) providerDetailClearField() (tea.Model, tea.Cmd) {
	if a.resolver == nil {
		return a, nil
	}
	p := a.providerDetailState.provider
	spec := credentials.Spec(p)
	if a.providerDetailState.fieldIdx < 0 || a.providerDetailState.fieldIdx >= len(spec) {
		return a, nil
	}
	f := spec[a.providerDetailState.fieldIdx]
	_ = a.resolver.Clear(p, f.Name, a.providerDetailState.backend)
	a.refreshProviderDetail()
	return a, a.refreshProvider()
}

func (a *App) providerDetailCycleBackend() (tea.Model, tea.Cmd) {
	if a.resolver == nil {
		return a, nil
	}
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
		if s == a.providerDetailState.backend {
			a.providerDetailState.backend = writable[(i+1)%len(writable)]
			return a, nil
		}
	}
	a.providerDetailState.backend = writable[0]
	return a, nil
}

func (a *App) refreshProviderDetail() {
	if a.resolver == nil {
		return
	}
	p := a.providerDetailState.provider
	if a.providerDetailState.sets == nil {
		a.providerDetailState.sets = map[string]credentials.Set{}
	}
	a.providerDetailState.sets[p] = a.resolver.Resolve(p)
	a.invalidateAvailability()
}

func (a *App) handleProviderDetailModelsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	name := a.providerDetailState.provider
	switch m.String() {
	case "/":
		a.providerDetailState.modelFiltering = true
		return a, nil
	case "up", "k":
		catalog := filterModels(a.catalogFor(name), a.providerDetailState.modelFilter)
		if a.providerDetailState.modelCursor > 0 {
			a.providerDetailState.modelCursor--
		} else if len(catalog) > 0 {
			a.providerDetailState.modelCursor = len(catalog) - 1
		}
		return a, nil
	case "down", "j":
		catalog := filterModels(a.catalogFor(name), a.providerDetailState.modelFilter)
		if len(catalog) > 0 && a.providerDetailState.modelCursor < len(catalog)-1 {
			a.providerDetailState.modelCursor++
		} else {
			a.providerDetailState.modelCursor = 0
		}
		return a, nil
	case "r":
		delete(a.catalogCache, name)
		delete(a.catalogErr, name)
		return a, a.fetchCatalogCmd(name)
	case "enter":
		return a, a.assignProviderModelToAgent(name)
	case "g":
		return a, a.assignProviderModelToClassifier(name)
	}
	return a, nil
}

func (a *App) assignProviderModelToAgent(provider string) tea.Cmd {
	catalog := filterModels(a.catalogFor(provider), a.providerDetailState.modelFilter)
	midx := clampIdx(a.providerDetailState.modelCursor, len(catalog))
	model := ""
	if len(catalog) > 0 {
		model = catalog[midx].ID
	}
	if model == "" {
		return nil
	}
	effort := a.settings.Effort
	scope := config.ScopeProject
	if err := config.Mutate(scope, a.workdir, func(s *config.Settings) error {
		s.Provider = provider
		s.Model = model
		s.Effort = effort
		return nil
	}); err != nil {
		a.providerDetailState.localReport = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.providerDetailState.localReport = err.Error()
		return nil
	}
	return a.applyModelProvider(provider, model, effort)
}

func (a *App) assignProviderModelToClassifier(provider string) tea.Cmd {
	catalog := filterModels(a.catalogFor(provider), a.providerDetailState.modelFilter)
	midx := clampIdx(a.providerDetailState.modelCursor, len(catalog))
	model := ""
	if len(catalog) > 0 {
		model = catalog[midx].ID
	}
	if model == "" {
		return nil
	}
	cls := a.settings.Classifier
	if cls == nil {
		cls = &config.ClassifierSettings{}
	}
	cls.Provider = provider
	cls.Model = model
	cls.Effort = "none"
	if err := config.Mutate(config.ScopeProject, a.workdir, func(s *config.Settings) error {
		s.Classifier = cls
		return nil
	}); err != nil {
		a.providerDetailState.localReport = err.Error()
		return nil
	}
	if err := a.reloadSettings(); err != nil {
		a.providerDetailState.localReport = err.Error()
		return nil
	}
	return a.refreshProvider()
}

func (a *App) handleProviderDetailServerKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.providerDetailEndpointEditing() {
		return a.handleProviderDetailEndpointKey(m)
	}
	switch m.String() {
	case "d":
		a.providerDetailState.repoMode = true
		a.providerDetailState.repoAction = repoActionDownload
		a.editor.Masked = false
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	case "l":
		a.providerDetailState.repoMode = true
		a.providerDetailState.repoAction = repoActionLaunch
		a.editor.Masked = false
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	case "x":
		return a, a.localModelStopCmd("")
	case "p":
		a.providerDetailState.localReportPending = true
		return a, a.localModelStatusCmd()
	}
	return a, nil
}

// handleProviderDetailEndpointKey drives the custom profile's endpoint editor.
func (a *App) handleProviderDetailEndpointKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.providerDetailState
	if len(st.endpointFields) == 0 {
		a.ensureEndpointFields()
	}
	switch m.String() {
	case "up", "k":
		if st.endpointFieldSel > 0 {
			st.endpointFieldSel--
		}
		return a, nil
	case "down", "j":
		if st.endpointFieldSel < len(st.endpointFields)-1 {
			st.endpointFieldSel++
		}
		return a, nil
	case "left", "h":
		return a.providerDetailEndpointCycle(-1)
	case "right", "l":
		return a.providerDetailEndpointCycle(1)
	case "enter", " ":
		return a.providerDetailEndpointEditOrCycle()
	case "c":
		return a.providerDetailEndpointCommit()
	}
	return a, nil
}

func (a *App) providerDetailEndpointEditOrCycle() (tea.Model, tea.Cmd) {
	st := &a.providerDetailState
	if st.endpointFieldSel < 0 || st.endpointFieldSel >= len(st.endpointFields) {
		return a, nil
	}
	f := st.endpointFields[st.endpointFieldSel]
	if f.kind == "cycle" {
		return a.providerDetailEndpointCycle(1)
	}
	a.editor.Reset()
	a.editor.Masked = false
	a.editor.SetValue(f.value)
	a.editor.CursorEnd()
	_ = a.editor.Focus()
	st.endpointEditing = true
	st.localReport = ""
	return a, nil
}

func (a *App) providerDetailEndpointCycle(dir int) (tea.Model, tea.Cmd) {
	st := &a.providerDetailState
	if st.endpointFieldSel < 0 || st.endpointFieldSel >= len(st.endpointFields) {
		return a, nil
	}
	f := &st.endpointFields[st.endpointFieldSel]
	if f.kind != "cycle" {
		return a, nil
	}
	idx := indexOfString(f.opts, f.value)
	f.value = f.opts[(idx+dir+len(f.opts))%len(f.opts)]
	return a, nil
}

// providerDetailEndpointCommitField saves an inline-edited endpoint field.
func (a *App) providerDetailEndpointCommitField() (tea.Model, tea.Cmd) {
	st := &a.providerDetailState
	val := a.editor.Value()
	a.editor.Reset()
	a.editor.Masked = false
	st.endpointEditing = false
	if st.endpointFieldSel < 0 || st.endpointFieldSel >= len(st.endpointFields) {
		return a, nil
	}
	st.endpointFields[st.endpointFieldSel].value = val
	return a, nil
}

// providerDetailEndpointCommit writes protocol/host/port/display name back to
// the custom profile and its label.
func (a *App) providerDetailEndpointCommit() (tea.Model, tea.Cmd) {
	st := &a.providerDetailState
	name := st.provider
	p, ok := a.settings.Providers[name]
	if !ok {
		st.localReport = "provider profile not found"
		return a, nil
	}
	get := func(k string) string {
		for _, f := range st.endpointFields {
			if f.key == k {
				return strings.TrimSpace(f.value)
			}
		}
		return ""
	}
	protocol := get("protocol")
	host := get("host")
	port := get("port")
	display := get("display")
	if host == "" {
		st.localReport = "host is required"
		return a, nil
	}
	if port != "" && !config.ValidOllamaPort(port) {
		st.localReport = fmt.Sprintf("invalid port %q", port)
		return a, nil
	}
	d, ok := provider.Template(p.Kind)
	if !ok {
		st.localReport = fmt.Sprintf("unknown kind %q", p.Kind)
		return a, nil
	}
	baseURL := d.BaseURLBuilder(map[string]string{"host": host, "port": port, "protocol": protocol})
	if display == "" {
		display = name
	}

	candidate := a.settings
	if candidate.Providers == nil {
		candidate.Providers = map[string]config.ProviderProfile{}
	}
	cp := p
	cp.Protocol = protocol
	cp.Host = host
	cp.Port = port
	cp.BaseURL = baseURL
	candidate.Providers[name] = cp
	if candidate.ProviderLabels == nil {
		candidate.ProviderLabels = map[string]string{}
	}
	candidate.ProviderLabels[name] = display
	if err := config.ValidateProviders(candidate); err != nil {
		st.localReport = err.Error()
		return a, nil
	}

	scope, found := a.providerProfileScope(name)
	if !found {
		scope = config.ScopeGlobal
	}
	if err := config.Mutate(scope, a.workdir, func(s *config.Settings) error {
		sp := s.Providers[name]
		sp.Protocol = protocol
		sp.Host = host
		sp.Port = port
		sp.BaseURL = baseURL
		s.Providers[name] = sp
		if s.ProviderLabels == nil {
			s.ProviderLabels = map[string]string{}
		}
		s.ProviderLabels[name] = display
		return nil
	}); err != nil {
		st.localReport = err.Error()
		return a, nil
	}
	if err := a.reloadSettings(); err != nil {
		st.localReport = err.Error()
		return a, nil
	}
	st.localReport = ""
	st.endpointFields = nil // rebuild from the persisted profile next render
	a.invalidateAvailability()
	return a, a.refreshProvider()
}

// providerProfileScope reports which settings file holds a profile definition.
func (a *App) providerProfileScope(name string) (config.Scope, bool) {
	if g, err := config.LoadGlobal(); err == nil {
		if _, ok := g.Providers[name]; ok {
			return config.ScopeGlobal, true
		}
	}
	if p, err := config.LoadProject(a.workdir); err == nil {
		if _, ok := p.Providers[name]; ok {
			return config.ScopeProject, true
		}
	}
	return "", false
}

// handleProviderStatusMessage renders a local-model report inside the server
// tab. It is registered as the receiver for localModelReportMsg.
func (a *App) handleProviderStatusMessage(m localModelReportMsg) tea.Cmd {
	a.providerDetailState.localReport = m.text
	a.providerDetailState.localReportPending = false
	return nil
}

// handleCatalogTarget and handleModelsFetched are already registered for the
// model picker; the provider detail models tab benefits from the same cache.

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
