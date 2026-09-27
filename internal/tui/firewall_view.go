package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/tui/components"
)

// firewallViewState is the /firewall screen: a list of every adapter and
// configured instance, a form for one instance, and a read-only panel for the
// provider-native entries.
type firewallViewState struct {
	selected int
	mode     string // "" list, "form", "info"
	form     firewallForm
	info     firewall.Adapter
	errorMsg string
}

// firewallForm edits one instance.
type firewallForm struct {
	name    string
	adapter firewall.Adapter
	isNew   bool
	fields  []providerNewField
	sel     int
	editing bool
}

// firewallRow is one line of the list. Headers are not selectable.
type firewallRow struct {
	kind     string // header | add | instance | adapter | native
	label    string
	instance string
	adapter  firewall.Adapter
}

func (a *App) enterFirewall() tea.Cmd {
	a.firewallState = firewallViewState{}
	a.firewallState.selected = a.firewallFirstSelectable()
	return nil
}

// firewallRows lists the screen: Vulnetix, the beta adapters (their
// instances, or the adapter itself while unconfigured), custom instances with
// the add row, then the provider-native entries.
func (a *App) firewallRows() []firewallRow {
	byAdapter := map[string][]string{}
	for _, name := range a.settings.FirewallInstanceNames() {
		inst, _ := a.settings.FirewallInstanceNamed(name)
		byAdapter[inst.Adapter] = append(byAdapter[inst.Adapter], name)
	}
	rows := []firewallRow{{kind: "header", label: "Vulnetix"}}
	vx, _ := firewall.Lookup("vulnetix")
	rows = append(rows, firewallRow{kind: "instance", instance: config.DefaultFirewall, adapter: vx, label: vx.Label()})
	rows = append(rows, firewallRow{kind: "header", label: "Beta — built from public docs, not yet tested"})
	var native []firewallRow
	for _, ad := range firewall.Adapters() {
		switch ad.Stability() {
		case firewall.Beta:
			names := byAdapter[ad.ID()]
			if len(names) == 0 {
				rows = append(rows, firewallRow{kind: "adapter", adapter: ad, label: ad.Label()})
			}
			for _, n := range names {
				rows = append(rows, firewallRow{kind: "instance", instance: n, adapter: ad, label: ad.Label() + " · " + n})
			}
		case firewall.Native:
			native = append(native, firewallRow{kind: "native", adapter: ad, label: ad.Label()})
		}
	}
	rows = append(rows, firewallRow{kind: "header", label: "Custom"})
	rows = append(rows, firewallRow{kind: "add", label: "+ Add custom firewall"})
	custom, _ := firewall.Lookup("custom")
	for _, n := range byAdapter["custom"] {
		rows = append(rows, firewallRow{kind: "instance", instance: n, adapter: custom, label: n})
	}
	rows = append(rows, firewallRow{kind: "header", label: "Provider-native — read-only, configured in their dashboards"})
	return append(rows, native...)
}

func (a *App) firewallFirstSelectable() int {
	for i, r := range a.firewallRows() {
		if r.kind != "header" {
			return i
		}
	}
	return 0
}

// firewallRowStatus is the right-hand status of an instance row.
func (a *App) firewallRowStatus(r firewallRow) string {
	switch r.kind {
	case "adapter":
		return components.MutedStyle.Render("not configured · " + r.adapter.Summary())
	case "native":
		if a.cfg.Provider == r.adapter.ID() {
			return components.AccentStyle.Render("applies now · ") + components.MutedStyle.Render(r.adapter.Summary())
		}
		return components.MutedStyle.Render("applies when " + r.adapter.ID() + " is the provider")
	case "instance":
		if a.resolver == nil {
			return ""
		}
		st := a.resolver.FirewallStateFor(r.instance, a.cfg.Provider)
		if st.Ready() {
			mode := st.Route.Mode
			return components.AccentStyle.Render("ready") + components.MutedStyle.Render(" for "+a.cfg.Provider+" · "+mode+" · "+firewall.HostOf(st.Route.BaseURL))
		}
		return components.MutedStyle.Render(truncateRunes(st.Reason, 90))
	}
	return ""
}

func (a *App) firewallView() string {
	w := a.contentWidth()
	st := &a.firewallState
	var b strings.Builder
	b.WriteString(components.SectionHeader("AI Firewall", "esc back", w))
	onOff := components.Chip("off", components.ColorMuted)
	if a.firewallEnabled() {
		onOff = components.Chip("on", components.ColorTeal)
	}
	b.WriteString(onOff + "  " + components.EmphStyle.Render("active: "+a.firewallLabel()) +
		components.MutedStyle.Render("  ·  F10 turns it on or off for this project") + "\n")
	if src := a.nonceSource(); src != "" {
		b.WriteString(components.MutedStyle.Render("nonces: "+src) + "\n")
	}
	b.WriteString("\n")

	switch st.mode {
	case "form":
		b.WriteString(a.firewallFormView(w))
	case "info":
		b.WriteString(a.firewallInfoView(st.info, w))
	default:
		active := a.settings.FirewallActive()
		for i, r := range a.firewallRows() {
			if r.kind == "header" {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(components.EmphStyle.Render(r.label) + "\n")
				continue
			}
			sel := i == st.selected
			label := r.label
			marker := "  "
			if r.kind == "instance" && r.instance == active {
				marker = components.AccentStyle.Render("● ")
			}
			if sel {
				label = components.AccentStyle.Bold(true).Render(label)
			}
			b.WriteString(components.Cursor(sel) + marker + fmt.Sprintf("%-34s", label) + " " + a.firewallRowStatus(r) + "\n")
		}
		if st.errorMsg != "" {
			b.WriteString("\n" + components.DangerStyle.Render("✗ "+st.errorMsg) + "\n")
		}
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "configure", "space", "use", "x", "delete", "?", "about", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// firewallInfoView renders an adapter's static instructions.
func (a *App) firewallInfoView(ad firewall.Adapter, w int) string {
	var b strings.Builder
	b.WriteString(components.EmphStyle.Render(ad.Label()) + components.MutedStyle.Render("  "+string(ad.Stability())) + "\n")
	b.WriteString(components.MutedStyle.Render(ad.Summary()) + "\n\n")
	for _, line := range ad.Instructions() {
		b.WriteString(lipgloss.NewStyle().Width(max(w-4, 20)).Render("• "+line) + "\n")
	}
	if ad.Stability() == firewall.Native {
		b.WriteString("\n" + components.MutedStyle.Render("Belai only reads this provider's refusals and shows them as cards; it changes nothing about the request.") + "\n")
	}
	b.WriteString("\n" + components.HelpBar("esc", "back") + "\n")
	return b.String()
}

// firewallFormView renders the instance form.
func (a *App) firewallFormView(w int) string {
	f := &a.firewallState.form
	var b strings.Builder
	title := f.adapter.Label()
	if f.name != "" && f.adapter.ID() != "vulnetix" {
		title += " · " + f.name
	}
	b.WriteString(components.EmphStyle.Render(title) + components.MutedStyle.Render("  "+string(f.adapter.Stability())) + "\n\n")
	for i, fld := range f.fields {
		if !a.firewallFieldVisible(fld) {
			continue
		}
		sel := i == f.sel
		val := fld.value
		switch {
		case fld.kind == "masked" && val != "":
			val = strings.Repeat("•", min(len(val), 12))
		case fld.kind == "masked" && a.firewallHasKey(f.name):
			val = components.MutedStyle.Render("stored · leave blank to keep")
		case fld.kind == "masked":
			val = components.MutedStyle.Render("none")
		case fld.kind == "cycle":
			val = "‹ " + val + " ›"
		case val == "":
			val = components.MutedStyle.Render("—")
		}
		label := fmt.Sprintf("%-12s", fld.label)
		if sel {
			label = components.AccentStyle.Bold(true).Render(label)
		}
		b.WriteString(components.Cursor(sel) + label + " " + val + "\n")
	}
	if f.editing {
		b.WriteString("\n" + a.renderFieldEditor(f.fields[f.sel].label, w) + "\n")
	}
	if a.firewallState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.firewallState.errorMsg) + "\n")
	}
	b.WriteString("\n")
	for _, line := range f.adapter.Instructions() {
		b.WriteString(lipgloss.NewStyle().Width(max(w-4, 20)).Foreground(components.ColorMuted).Render("• "+line) + "\n")
	}
	if !f.editing {
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "edit", "←→", "cycle", "c", "save", "esc", "cancel") + "\n")
	}
	return b.String()
}

func (a *App) firewallHasKey(name string) bool {
	return name != "" && a.resolver != nil && a.resolver.HasFirewallSecret(name)
}

// firewallFieldVisible hides the header field outside header mode.
func (a *App) firewallFieldVisible(fld providerNewField) bool {
	if fld.key != "header" {
		return true
	}
	return a.firewallFormValue("mode") == config.FirewallModeHeader
}

func (a *App) firewallFormValue(key string) string {
	for _, f := range a.firewallState.form.fields {
		if f.key == key {
			return f.value
		}
	}
	return ""
}

// openFirewallForm seeds the form for an instance (existing or new).
func (a *App) openFirewallForm(name string, ad firewall.Adapter, isNew bool) {
	inst, ok := a.settings.FirewallInstanceNamed(name)
	if !ok {
		inst = ad.Defaults()
	}
	inst = firewall.WithDefaults(inst)
	f := firewallForm{name: name, adapter: ad, isNew: isNew}
	if isNew && ad.ID() == "custom" {
		f.fields = append(f.fields, providerNewField{key: "name", label: "Name", kind: "text", value: name})
	}
	f.fields = append(f.fields, providerNewField{key: "url", label: "URL", kind: "text", value: inst.URL})
	if modes := ad.Modes(); len(modes) > 0 {
		f.fields = append(f.fields,
			providerNewField{key: "mode", label: "Mode", kind: "cycle", opts: modes, value: inst.Mode},
			providerNewField{key: "header", label: "Header", kind: "text", value: inst.Header},
			providerNewField{key: "key", label: "Key", kind: "masked"},
			providerNewField{key: "providers", label: "Providers", kind: "text", value: strings.Join(inst.Providers, ", ")},
		)
	}
	a.firewallState.form = f
	a.firewallState.mode = "form"
	a.firewallState.errorMsg = ""
}

// firewallFormMove steps the form cursor over visible fields.
func (a *App) firewallFormMove(dir int) {
	f := &a.firewallState.form
	for i := f.sel + dir; i >= 0 && i < len(f.fields); i += dir {
		if a.firewallFieldVisible(f.fields[i]) {
			f.sel = i
			return
		}
	}
}

func (a *App) handleFirewallKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.firewallState
	key := m.String()
	switch st.mode {
	case "info":
		if key == "esc" || key == "enter" || key == "q" {
			st.mode = ""
		}
		return a, nil
	case "form":
		return a.handleFirewallFormKey(m)
	}

	rows := a.firewallRows()
	switch key {
	case "esc":
		a.pop()
	case "up", "k":
		for i := st.selected - 1; i >= 0; i-- {
			if rows[i].kind != "header" {
				st.selected = i
				break
			}
		}
	case "down", "j":
		for i := st.selected + 1; i < len(rows); i++ {
			if rows[i].kind != "header" {
				st.selected = i
				break
			}
		}
	case "?":
		if st.selected < len(rows) && rows[st.selected].adapter != nil {
			st.info, st.mode = rows[st.selected].adapter, "info"
		}
	case "enter":
		if st.selected >= len(rows) {
			return a, nil
		}
		r := rows[st.selected]
		switch r.kind {
		case "add":
			ad, _ := firewall.Lookup("custom")
			a.openFirewallForm("", ad, true)
		case "adapter":
			a.openFirewallForm(r.adapter.ID(), r.adapter, true)
		case "instance":
			a.openFirewallForm(r.instance, r.adapter, false)
		case "native":
			st.info, st.mode = r.adapter, "info"
		}
	case " ":
		if st.selected < len(rows) && rows[st.selected].kind == "instance" {
			return a, a.activateFirewall(rows[st.selected].instance)
		}
		if st.selected < len(rows) && rows[st.selected].kind == "native" {
			st.errorMsg = rows[st.selected].adapter.Label() + " is read-only: configure it in its dashboard (enter for how)"
		}
	case "x":
		if st.selected < len(rows) && rows[st.selected].kind == "instance" && rows[st.selected].instance != config.DefaultFirewall {
			if err := a.deleteFirewall(rows[st.selected].instance); err != nil {
				st.errorMsg = err.Error()
			} else {
				st.errorMsg = ""
				st.selected = min(st.selected, len(a.firewallRows())-1)
			}
		}
	}
	return a, nil
}

func (a *App) handleFirewallFormKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.firewallState
	f := &st.form
	key := m.String()
	if f.editing {
		switch key {
		case "esc":
			f.editing = false
			a.editor.Reset()
			a.editor.Masked = false
		case "enter":
			f.fields[f.sel].value = strings.TrimSpace(a.editor.Value())
			f.editing = false
			a.editor.Reset()
			a.editor.Masked = false
		default:
			return a, a.editor.Update(m)
		}
		return a, nil
	}
	switch key {
	case "esc":
		st.mode, st.errorMsg = "", ""
	case "up", "k":
		a.firewallFormMove(-1)
	case "down", "j":
		a.firewallFormMove(1)
	case "left", "right":
		fld := &f.fields[f.sel]
		if fld.kind == "cycle" && len(fld.opts) > 0 {
			dir := 1
			if key == "left" {
				dir = -1
			}
			idx := indexOfString(fld.opts, fld.value)
			fld.value = fld.opts[(idx+dir+len(fld.opts))%len(fld.opts)]
		}
	case "enter", " ":
		fld := &f.fields[f.sel]
		if fld.kind == "cycle" {
			idx := indexOfString(fld.opts, fld.value)
			fld.value = fld.opts[(idx+1)%len(fld.opts)]
			return a, nil
		}
		a.editor.Reset()
		a.editor.Masked = fld.kind == "masked"
		if fld.kind != "masked" {
			a.editor.SetValue(fld.value)
			a.editor.CursorEnd()
		}
		_ = a.editor.Focus()
		f.editing, st.errorMsg = true, ""
	case "c":
		return a, a.commitFirewallForm()
	}
	return a, nil
}

// commitFirewallForm validates and saves the instance to the global
// settings, and its key (if typed) to the credentials store.
func (a *App) commitFirewallForm() tea.Cmd {
	st := &a.firewallState
	f := st.form
	name := f.name
	if f.isNew && f.adapter.ID() == "custom" {
		name = strings.ToLower(strings.TrimSpace(a.firewallFormValue("name")))
	}
	inst := config.FirewallInstance{Adapter: f.adapter.ID(), URL: a.firewallFormValue("url")}
	if len(f.adapter.Modes()) > 0 {
		inst.Mode = a.firewallFormValue("mode")
		if inst.Mode == config.FirewallModeHeader {
			inst.Header = a.firewallFormValue("header")
		}
		for _, p := range strings.Split(a.firewallFormValue("providers"), ",") {
			if p = strings.TrimSpace(p); p != "" {
				inst.Providers = append(inst.Providers, p)
			}
		}
	}
	if inst.Adapter == "vulnetix" && inst.URL == config.DefaultVulnetixGateway {
		inst.URL = ""
	}
	if err := config.ValidateFirewallInstance(name, inst); err != nil {
		st.errorMsg = err.Error()
		return nil
	}
	if f.isNew {
		if _, exists := a.settings.FirewallInstanceNamed(name); exists && name != f.name {
			st.errorMsg = "a firewall named " + name + " already exists"
			return nil
		}
	}
	key := a.firewallFormValue("key")
	if key == "" && firewall.NeedsSecret(inst) && !a.firewallHasKey(name) {
		st.errorMsg = "this mode sends a firewall key: enter one (it is stored in the keychain, or your user credentials file)"
		return nil
	}
	oldRoute := ""
	if a.cfg.Firewall != nil {
		oldRoute = a.cfg.BaseURL
	}
	err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Firewall == nil {
			s.Firewall = &config.FirewallSettings{}
		}
		if s.Firewall.Instances == nil {
			s.Firewall.Instances = map[string]config.FirewallInstance{}
		}
		if inst.Adapter == "vulnetix" && inst.URL == "" {
			delete(s.Firewall.Instances, name)
			if s.Vulnetix != nil {
				s.Vulnetix.GatewayURL = ""
			}
		} else {
			s.Firewall.Instances[name] = inst
		}
		if len(s.Firewall.Instances) == 0 {
			s.Firewall.Instances = nil
		}
		return config.ValidateFirewall(*s)
	})
	if err != nil {
		st.errorMsg = err.Error()
		return nil
	}
	if key != "" && a.resolver != nil {
		if err := a.resolver.StoreFirewallSecret(name, key); err != nil {
			st.errorMsg = "saved, but the key could not be stored: " + err.Error()
			return nil
		}
	}
	_ = a.reloadSettings()
	if cfg, err := a.resolveConfig(); err == nil {
		a.cfg = cfg
	}
	if oldRoute != "" {
		nonce.ForgetEndpoint(oldRoute)
	}
	st.mode, st.errorMsg = "", ""
	a.refreshFooter()
	a.addSystem("firewall " + name + " saved")
	return nil
}

// activateFirewall makes an instance the active firewall and turns the
// firewall on. Turning on goes through toggleFirewall, which refuses with the
// real reason when the instance cannot route the current provider.
func (a *App) activateFirewall(name string) tea.Cmd {
	if _, ok := a.settings.FirewallInstanceNamed(name); !ok {
		a.addSystem("no firewall named " + name + " — see /firewall")
		return nil
	}
	if a.settings.FirewallActive() == name && a.firewallEnabled() {
		return a.toggleFirewall()
	}
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Firewall == nil {
			s.Firewall = &config.FirewallSettings{}
		}
		s.Firewall.Active = name
		if name == config.DefaultFirewall {
			s.Firewall.Active = ""
		}
		return nil
	}); err != nil {
		a.firewallState.errorMsg = err.Error()
		return nil
	}
	_ = a.reloadSettings()
	if !a.firewallEnabled() {
		return a.toggleFirewall()
	}
	if cfg, err := a.resolveConfig(); err == nil {
		a.cfg = cfg
	}
	a.refreshFooter()
	a.addSystem(a.firewallOnMessage())
	return a.firewallActivated()
}

// deleteFirewall removes an instance and its key. Deleting the active one
// falls back to Vulnetix.
func (a *App) deleteFirewall(name string) error {
	err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Firewall == nil {
			return nil
		}
		delete(s.Firewall.Instances, name)
		if len(s.Firewall.Instances) == 0 {
			s.Firewall.Instances = nil
		}
		if s.Firewall.Active == name {
			s.Firewall.Active = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	if a.resolver != nil {
		a.resolver.ClearFirewallSecret(name)
	}
	_ = a.reloadSettings()
	if cfg, err := a.resolveConfig(); err == nil {
		a.cfg = cfg
	}
	a.refreshFooter()
	a.addSystem("firewall " + name + " deleted")
	return nil
}

// firewallCommand implements /firewall [on|off|use NAME|status].
func (a *App) firewallCommand(arg string) tea.Cmd {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return a.push(viewFirewall)
	}
	switch fields[0] {
	case "on", "off":
		if (fields[0] == "on") != a.firewallEnabled() {
			return a.toggleFirewall()
		}
		a.addSystem(a.firewallLabel() + " is already " + fields[0])
	case "use":
		if len(fields) < 2 {
			a.addSystem("usage: /firewall use NAME")
			return nil
		}
		return a.activateFirewall(fields[1])
	case "status":
		a.addSystem(a.firewallStatusLine())
	default:
		a.addSystem("usage: /firewall [on|off|use NAME|status]")
	}
	return nil
}

// vulnetixFirewallCommand is /vulnetix firewall: it configures the Vulnetix
// adapter through the generic path — making it active, or toggling it when it
// already is.
func (a *App) vulnetixFirewallCommand() tea.Cmd {
	if a.settings.FirewallActive() != config.DefaultFirewall {
		return a.activateFirewall(config.DefaultFirewall)
	}
	return a.toggleFirewall()
}

// firewallStatusLine summarises the firewall for /firewall status.
func (a *App) firewallStatusLine() string {
	parts := []string{a.firewallLabel(), onOffLabel(a.firewallEnabled())}
	if a.resolver != nil {
		st := a.resolver.FirewallState(a.cfg.Provider)
		if st.Ready() {
			parts = append(parts, "routes "+a.cfg.Provider+" via "+firewall.HostOf(st.Route.BaseURL)+" ("+st.Route.Mode+")")
		} else {
			parts = append(parts, st.Reason)
		}
	}
	if src := a.nonceSource(); src != "" {
		parts = append(parts, "nonces "+src)
	}
	if a.fwEventCount > 0 {
		parts = append(parts, fmt.Sprintf("%d events this session", a.fwEventCount))
	}
	if p := a.fwPasses.Load(); p > 0 {
		parts = append(parts, fmt.Sprintf("%d clean responses", p))
	}
	return strings.Join(parts, " · ")
}

// truncateRunes caps s at n runes with an ellipsis.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
