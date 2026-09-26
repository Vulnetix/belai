package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/tui/components"
)

// providerNewViewState drives the "+ add new provider" form.
type providerNewViewState struct {
	fields      []providerNewField
	fieldSel    int
	editing     bool
	backend     credentials.Source
	displayAuto bool
	nameAuto    bool
	errorMsg    string
}

// providerNewField is one editable form field.
type providerNewField struct {
	key   string
	label string
	kind  string // cycle | text | masked
	opts  []string
	value string
}

// providerNewKinds are the selectable template kinds, in cycle order.
var providerNewKinds = []string{"ollama", "llama-server", "openai-compatible"}

// openProviderNew seeds the add-new form with template defaults.
func (a *App) openProviderNew() {
	backend := credentials.SourceUserFile
	if be := a.writableBackends(); len(be) > 0 {
		backend = be[0]
	}
	a.providerNewState = providerNewViewState{
		backend:     backend,
		displayAuto: true,
		nameAuto:    true,
		errorMsg:    "",
	}
	a.providerNewState.fields = a.buildProviderNewFields("ollama", "http", "localhost", "11434", backend)
}

// buildProviderNewFields assembles the form rows from explicit values.
func (a *App) buildProviderNewFields(kind, protocol, host, port string, backend credentials.Source) []providerNewField {
	display := host
	if port != "" {
		display = host + ":" + port
	}
	slug := deriveProviderSlug(display)
	return []providerNewField{
		{key: "kind", label: "kind", kind: "cycle", opts: providerNewKinds, value: kind},
		{key: "protocol", label: "protocol", kind: "cycle", opts: []string{"http", "https"}, value: protocol},
		{key: "host", label: "host", kind: "text", value: host},
		{key: "port", label: "port", kind: "text", value: port},
		{key: "display", label: "display name", kind: "text", value: display},
		{key: "name", label: "name (slug)", kind: "text", value: slug},
		{key: "api_key", label: "api key", kind: "masked", value: ""},
		{key: "backend", label: "storing to", kind: "cycle", opts: backendNames(a.writableBackends()), value: string(backend)},
	}
}

// writableBackends returns the writable, available credential backends in
// order, so the form and the detail page share one list.
func (a *App) writableBackends() []credentials.Source {
	if a.resolver == nil {
		return nil
	}
	var out []credentials.Source
	for _, be := range a.resolver.Backends() {
		if be.Writable && be.Available {
			out = append(out, credentials.Source(be.Name))
		}
	}
	return out
}

func backendNames(bs []credentials.Source) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, string(b))
	}
	return out
}

// deriveProviderSlug turns a display name into a ValidCustomName-safe slug.
func deriveProviderSlug(display string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(display)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if s == "" {
		return ""
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "p-" + s
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func (a *App) providerNewFieldValue(key string) string {
	for _, f := range a.providerNewState.fields {
		if f.key == key {
			return f.value
		}
	}
	return ""
}

func (a *App) setProviderNewField(key, value string) {
	for i := range a.providerNewState.fields {
		if a.providerNewState.fields[i].key == key {
			a.providerNewState.fields[i].value = value
			return
		}
	}
}

// applyProviderNewKind applies a kind selection and its port prefill, then
// refreshes the auto-derived display name and slug.
func (a *App) applyProviderNewKind(kind string) {
	a.setProviderNewField("kind", kind)
	port := a.providerNewFieldValue("port")
	switch kind {
	case "ollama":
		if port == "" || port == "8080" {
			a.setProviderNewField("port", "11434")
		}
	case "llama-server":
		if port == "" || port == "11434" {
			a.setProviderNewField("port", "8080")
		}
	default:
		a.setProviderNewField("port", "")
	}
	a.refreshProviderNewDerived()
}

// refreshProviderNewDerived recomputes the display name and slug when they
// have not been manually edited.
func (a *App) refreshProviderNewDerived() {
	if a.providerNewState.displayAuto {
		host := strings.TrimSpace(a.providerNewFieldValue("host"))
		port := strings.TrimSpace(a.providerNewFieldValue("port"))
		display := host
		if port != "" {
			display = host + ":" + port
		}
		a.setProviderNewField("display", display)
	}
	if a.providerNewState.nameAuto {
		a.setProviderNewField("name", deriveProviderSlug(a.providerNewFieldValue("display")))
	}
}

func (a *App) providerNewView() string {
	w := a.contentWidth()
	st := &a.providerNewState

	var b strings.Builder
	b.WriteString(components.SectionHeader("Add Provider", "esc back", w))
	b.WriteString(components.MutedStyle.Render("A templated instance of a built-in provider. Written to global settings.json.") + "\n\n")

	if st.editing {
		row := st.fields[st.fieldSel]
		title := row.label
		if row.kind == "masked" {
			title += " (hidden)"
		}
		b.WriteString(a.renderFieldEditor(title, w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	for i, f := range st.fields {
		selected := i == st.fieldSel
		label := fmt.Sprintf("%-14s", f.label)
		value := f.value
		if f.kind == "masked" && value != "" {
			value = strings.Repeat("•", len(value))
		}
		labelOut := components.MutedStyle.Render(label)
		valueOut := value
		if selected {
			labelOut = components.AccentStyle.Bold(true).Render(label)
			valueOut = components.EmphStyle.Render(value)
		} else if f.kind == "cycle" {
			valueOut = components.MutedStyle.Render(value)
		}
		marker := components.Cursor(selected)
		b.WriteString(marker + labelOut + "  " + valueOut + "\n")
	}

	if st.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+st.errorMsg) + "\n")
	}

	b.WriteString("\n" + components.HelpBar(
		"↑↓", "move", "enter/space", "edit·cycle", "←/→", "cycle", "c", "commit", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) handleProviderNewKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.providerNewState
	if st.editing {
		switch m.String() {
		case "esc":
			st.editing = false
			a.editor.Reset()
			a.editor.Masked = false
			return a, nil
		case "enter":
			return a.providerNewCommitField()
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if st.fieldSel > 0 {
			st.fieldSel--
		}
		return a, nil
	case "down", "j":
		if st.fieldSel < len(st.fields)-1 {
			st.fieldSel++
		}
		return a, nil
	case "left", "h":
		return a.providerNewCycleField(-1)
	case "right", "l":
		return a.providerNewCycleField(1)
	case "enter", " ":
		return a.providerNewEditOrCycle()
	case "c":
		return a.providerNewCommit()
	}
	return a, nil
}

// providerNewEditOrCycle opens a text/masked field for inline editing, or
// cycles a choice field forward.
func (a *App) providerNewEditOrCycle() (tea.Model, tea.Cmd) {
	st := &a.providerNewState
	if st.fieldSel < 0 || st.fieldSel >= len(st.fields) {
		return a, nil
	}
	f := st.fields[st.fieldSel]
	if f.kind == "cycle" {
		return a.providerNewCycleField(1)
	}
	a.editor.Reset()
	a.editor.Masked = f.kind == "masked"
	a.editor.SetValue(f.value)
	a.editor.CursorEnd()
	_ = a.editor.Focus()
	st.editing = true
	st.errorMsg = ""
	return a, nil
}

// providerNewCycleField steps a choice field in direction dir.
func (a *App) providerNewCycleField(dir int) (tea.Model, tea.Cmd) {
	st := &a.providerNewState
	if st.fieldSel < 0 || st.fieldSel >= len(st.fields) {
		return a, nil
	}
	f := &st.fields[st.fieldSel]
	if f.kind != "cycle" {
		return a, nil
	}
	if len(f.opts) == 0 {
		return a, nil
	}
	idx := indexOfString(f.opts, f.value)
	next := f.opts[(idx+dir+len(f.opts))%len(f.opts)]
	switch f.key {
	case "kind":
		a.applyProviderNewKind(next)
	case "backend":
		st.backend = credentials.Source(next)
		f.value = next
	default:
		f.value = next
	}
	st.errorMsg = ""
	return a, nil
}

// providerNewCommitField saves an inline-edited field value.
func (a *App) providerNewCommitField() (tea.Model, tea.Cmd) {
	st := &a.providerNewState
	val := a.editor.Value()
	a.editor.Reset()
	a.editor.Masked = false
	st.editing = false
	if st.fieldSel < 0 || st.fieldSel >= len(st.fields) {
		return a, nil
	}
	f := &st.fields[st.fieldSel]
	f.value = val
	switch f.key {
	case "display":
		st.displayAuto = false
		if st.nameAuto {
			a.setProviderNewField("name", deriveProviderSlug(val))
		}
	case "name":
		st.nameAuto = false
	case "host", "port":
		a.refreshProviderNewDerived()
	case "kind":
		a.applyProviderNewKind(val)
	}
	st.errorMsg = ""
	return a, nil
}

// providerNewCommit validates and persists the new provider profile and label.
func (a *App) providerNewCommit() (tea.Model, tea.Cmd) {
	st := &a.providerNewState
	kind := strings.TrimSpace(a.providerNewFieldValue("kind"))
	protocol := strings.TrimSpace(a.providerNewFieldValue("protocol"))
	host := strings.TrimSpace(a.providerNewFieldValue("host"))
	port := strings.TrimSpace(a.providerNewFieldValue("port"))
	display := strings.TrimSpace(a.providerNewFieldValue("display"))
	slug := strings.TrimSpace(a.providerNewFieldValue("name"))
	apiKey := a.providerNewFieldValue("api_key")

	if host == "" {
		st.errorMsg = "host is required"
		return a, nil
	}
	if port != "" && !config.ValidOllamaPort(port) {
		st.errorMsg = fmt.Sprintf("invalid port %q", port)
		return a, nil
	}
	if !provider.ValidCustomName(slug) {
		st.errorMsg = fmt.Sprintf("invalid provider name %q (lowercase, a-z first, then a-z 0-9 . _ -)", slug)
		return a, nil
	}
	if provider.Builtin(slug) {
		st.errorMsg = fmt.Sprintf("name %q collides with a built-in provider", slug)
		return a, nil
	}
	if _, exists := a.settings.Providers[slug]; exists {
		st.errorMsg = fmt.Sprintf("provider %q already exists", slug)
		return a, nil
	}
	if display == "" {
		display = slug
	}

	d, ok := provider.Template(kind)
	if !ok {
		st.errorMsg = fmt.Sprintf("unknown kind %q", kind)
		return a, nil
	}
	baseURL := d.BaseURLBuilder(map[string]string{"host": host, "port": port, "protocol": protocol})

	prof := config.ProviderProfile{
		BaseURL:  baseURL,
		API:      d.Surface,
		Auth:     string(d.Auth),
		Kind:     kind,
		Protocol: protocol,
		Host:     host,
		Port:     port,
	}

	// Validate the whole resulting settings shape (profile + label) before
	// writing, so a bad label or profile is rejected exactly as a hand-edited
	// settings.json would be.
	candidate := a.settings
	if candidate.Providers == nil {
		candidate.Providers = map[string]config.ProviderProfile{}
	}
	if candidate.ProviderLabels == nil {
		candidate.ProviderLabels = map[string]string{}
	}
	candidate.Providers[slug] = prof
	candidate.ProviderLabels[slug] = display
	if err := config.ValidateProviders(candidate); err != nil {
		st.errorMsg = err.Error()
		return a, nil
	}

	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Providers == nil {
			s.Providers = map[string]config.ProviderProfile{}
		}
		if s.ProviderLabels == nil {
			s.ProviderLabels = map[string]string{}
		}
		s.Providers[slug] = prof
		s.ProviderLabels[slug] = display
		return nil
	}); err != nil {
		st.errorMsg = err.Error()
		return a, nil
	}
	if apiKey != "" && a.resolver != nil {
		if err := a.resolver.Store(slug, "api_key", apiKey, st.backend); err != nil {
			st.errorMsg = err.Error()
			return a, nil
		}
	}
	if err := a.reloadSettings(); err != nil {
		st.errorMsg = err.Error()
		return a, nil
	}
	a.invalidateAvailability()
	a.openProviderDetail(slug)
	cmds := []tea.Cmd{a.push(viewProviderDetail), a.prefetchCatalogCmd(slug)}
	if apiKey != "" {
		cmds = append(cmds, a.syncFirewallKey(slug))
	}
	return a, tea.Batch(cmds...)
}
