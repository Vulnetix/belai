package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/tui/components"
)

// mcpViewState is the /mcp screen: the built-in servers' switches, every server
// of your global settings grouped as local (stdio) or remote (http), and a form
// to add or edit one. Secrets go to the keychain or the global credentials file,
// never to the settings; the settings hold a cred:KEY reference.
type mcpViewState struct {
	selected int
	mode     string // "" list, "form"
	form     mcpForm
	errorMsg string
	// confirm is the server a first x press marked for deletion.
	confirm string
}

// mcpForm edits one server.
type mcpForm struct {
	name    string
	isNew   bool
	fields  []providerNewField
	sel     int
	editing bool
}

// mcpRow is one line of the list. Headers are not selectable.
type mcpRow struct {
	kind   string // header | builtin | add | server
	label  string
	server string
	row    settingsRow // builtin rows
}

// Settings keys the form edits.
const (
	mcpFieldName      = "name"
	mcpFieldTransport = "transport"
	mcpFieldCommand   = "command"
	mcpFieldArgs      = "args"
	mcpFieldURL       = "url"
	mcpFieldPairs     = "pairs"
	mcpFieldTools     = "tools"
	mcpFieldTimeout   = "timeout"
	mcpFieldSandbox   = "sandbox"
	mcpFieldSecretKey = "secret_key"
	mcpFieldSecret    = "secret"
)

func (a *App) enterMCP() tea.Cmd {
	a.mcpState = mcpViewState{}
	a.mcpState.selected = a.mcpFirstSelectable()
	return nil
}

// mcpServerNames lists the settings' servers, sorted; the built-in name is
// never one.
func (a *App) mcpServerNames() []string {
	var names []string
	if a.settings.MCP != nil {
		for n := range a.settings.MCP.Servers {
			if n != config.ClefMCPName {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return names
}

func mcpIsRemote(s config.MCPServer) bool { return s.Transport == "http" }

// mcpRowsList is the list: built-in switches, then local and remote servers.
func (a *App) mcpRowsList() []mcpRow {
	rows := []mcpRow{{kind: "header", label: "Built-in"}}
	for _, r := range a.mcpRows() {
		rows = append(rows, mcpRow{kind: "builtin", label: r.label, row: r.settingsRow})
	}
	var local, remote []string
	for _, n := range a.mcpServerNames() {
		if mcpIsRemote(a.settings.MCP.Servers[n]) {
			remote = append(remote, n)
		} else {
			local = append(local, n)
		}
	}
	rows = append(rows, mcpRow{kind: "header", label: "Servers"}, mcpRow{kind: "add", label: "+ Add a server"})
	if len(local) > 0 {
		rows = append(rows, mcpRow{kind: "header", label: "Local (run a command on this machine)"})
		for _, n := range local {
			rows = append(rows, mcpRow{kind: "server", server: n, label: n})
		}
	}
	if len(remote) > 0 {
		rows = append(rows, mcpRow{kind: "header", label: "Remote (reach a URL)"})
		for _, n := range remote {
			rows = append(rows, mcpRow{kind: "server", server: n, label: n})
		}
	}
	return rows
}

func (a *App) mcpFirstSelectable() int {
	for i, r := range a.mcpRowsList() {
		if r.kind != "header" {
			return i
		}
	}
	return 0
}

// mcpStatusOf describes one running or failed server for its row.
func mcpStatusOf(name string) (state, detail string) {
	for _, s := range mcp.Active().Status() {
		if s.Name != name {
			continue
		}
		detail = fmt.Sprintf("%d tools", len(s.Tools))
		if s.Err != "" {
			detail = mcpClean(s.Err, 80)
		}
		if s.Diag != "" && s.State == mcp.StateDisabled {
			detail = mcpClean(s.Diag, 80)
		}
		return s.State, detail
	}
	return "not started", "starts with the next session"
}

func (a *App) mcpView() string {
	w := a.contentWidth()
	st := &a.mcpState
	var b strings.Builder
	b.WriteString(components.SectionHeader("MCP servers", "esc back", w))
	b.WriteString(components.MutedStyle.Render("saved to "+a.scopeTarget("global")+" · secrets in the keychain or your credentials file") + "\n\n")
	if st.mode == "form" {
		b.WriteString(a.mcpFormView(w))
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}
	for i, r := range a.mcpRowsList() {
		if r.kind == "header" {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(components.EmphStyle.Render(r.label) + "\n")
			continue
		}
		sel := i == st.selected
		label := fmt.Sprintf("%-26s", truncateRunes(r.label, 26))
		if sel {
			label = components.AccentStyle.Bold(true).Render(label)
		}
		var status string
		switch r.kind {
		case "builtin":
			status = r.row.value
			if r.row.note != "" {
				status += components.MutedStyle.Render(" · " + r.row.note)
			}
			if r.row.disabled {
				status = components.MutedStyle.Render(r.row.value + " · needs the decider on")
			}
		case "server":
			state, detail := mcpStatusOf(r.server)
			chip := components.Chip(state, mcpStateColor(state))
			if a.settings.MCP.Servers[r.server].Disabled {
				chip = components.Chip("disabled", components.ColorMuted)
			}
			status = chip + " " + components.MutedStyle.Render(detail)
		case "add":
			status = components.MutedStyle.Render("a local command or a remote URL")
		}
		b.WriteString(components.Cursor(sel) + label + " " + ansi.Truncate(status, max(w-32, 10), "…") + "\n")
	}
	if st.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+st.errorMsg) + "\n")
	}
	if st.confirm != "" {
		b.WriteString("\n" + components.WarnStyle.Render("press x again to delete "+st.confirm+" and its stored secrets") + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "edit or switch", "space", "on/off", "r", "restart", "x", "delete", "esc", "back") + "\n")
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func mcpStateColor(state string) lipgloss.TerminalColor {
	switch state {
	case mcp.StateRunning:
		return components.ColorTeal
	case mcp.StateFailed:
		return components.ColorAmber
	}
	return components.ColorMuted
}

// mcpFormTransport is the form's current transport.
func (a *App) mcpFormValue(key string) string {
	for _, f := range a.mcpState.form.fields {
		if f.key == key {
			return f.value
		}
	}
	return ""
}

// mcpFieldVisible hides the fields that belong to the other transport.
func (a *App) mcpFieldVisible(f providerNewField) bool {
	remote := a.mcpFormValue(mcpFieldTransport) == "http"
	switch f.key {
	case mcpFieldCommand, mcpFieldArgs, mcpFieldSandbox:
		return !remote
	case mcpFieldURL:
		return remote
	}
	return true
}

func (a *App) mcpFormView(w int) string {
	f := &a.mcpState.form
	var b strings.Builder
	title := "Add a server"
	if !f.isNew {
		title = f.name
	}
	b.WriteString(components.EmphStyle.Render(title) + "\n\n")
	for i, fld := range f.fields {
		if !a.mcpFieldVisible(fld) {
			continue
		}
		sel := i == f.sel
		val := fld.value
		label := fld.label
		if fld.key == mcpFieldPairs {
			label = "Env"
			if a.mcpFormValue(mcpFieldTransport) == "http" {
				label = "Headers"
			}
		}
		switch {
		case fld.kind == "masked" && val != "":
			val = strings.Repeat("•", min(len(val), 12))
		case fld.kind == "masked":
			if k := a.mcpFormValue(mcpFieldSecretKey); !f.isNew && k != "" && a.resolver != nil && a.resolver.HasMCPSecret(f.name, k) {
				val = components.MutedStyle.Render("stored · leave blank to keep")
			} else {
				val = components.MutedStyle.Render("none")
			}
		case fld.kind == "cycle":
			val = "‹ " + val + " ›"
		case val == "":
			val = components.MutedStyle.Render("—")
		}
		l := fmt.Sprintf("%-12s", label)
		if sel {
			l = components.AccentStyle.Bold(true).Render(l)
		}
		b.WriteString(components.Cursor(sel) + l + " " + val + "\n")
	}
	if f.editing {
		b.WriteString("\n" + a.renderFieldEditor(f.fields[f.sel].label, w) + "\n")
	}
	if a.mcpState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.mcpState.errorMsg) + "\n")
	}
	help := []string{
		"Env or headers: NAME=value pairs separated by commas. A value is env:VAR, cred:KEY (a secret stored here), vault:NAME (a Secrets Vault entry, headers only), or plain text for a name that is not a secret.",
		"Secret key and value: type a key (for example token), then its value; it is stored in the keychain, never in the settings, and read back by cred:token.",
		"Args are split on spaces. Tools, when set, offers only those server tools.",
	}
	b.WriteString("\n")
	for _, line := range help {
		b.WriteString(lipgloss.NewStyle().Width(max(w-4, 20)).Foreground(components.ColorMuted).Render("• "+line) + "\n")
	}
	if !f.editing {
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "enter", "edit", "←→", "cycle", "c", "save", "esc", "cancel") + "\n")
	}
	return b.String()
}

// mcpFormFor seeds the form for a server (existing or new).
func (a *App) mcpFormFor(name string, isNew bool) {
	var srv config.MCPServer
	if !isNew && a.settings.MCP != nil {
		srv = a.settings.MCP.Servers[name]
	}
	transport := srv.Transport
	if transport == "" {
		transport = "stdio"
	}
	pairs := srv.Env
	if transport == "http" {
		pairs = srv.Headers
	}
	timeout := ""
	if srv.TimeoutMS > 0 {
		timeout = strconv.Itoa(srv.TimeoutMS)
	}
	sandbox := "off"
	if srv.Sandbox {
		sandbox = "on"
	}
	// The secret key defaults to the one the server already references.
	secretKey := ""
	for _, k := range mcpCredKeys(srv) {
		secretKey = k
		break
	}
	f := mcpForm{name: name, isNew: isNew}
	if isNew {
		f.fields = append(f.fields, providerNewField{key: mcpFieldName, label: "Name", kind: "text"})
	}
	f.fields = append(f.fields,
		providerNewField{key: mcpFieldTransport, label: "Where", kind: "cycle", opts: []string{"stdio", "http"}, value: transport},
		providerNewField{key: mcpFieldCommand, label: "Command", kind: "text", value: srv.Command},
		providerNewField{key: mcpFieldArgs, label: "Args", kind: "text", value: strings.Join(srv.Args, " ")},
		providerNewField{key: mcpFieldURL, label: "URL", kind: "text", value: srv.URL},
		providerNewField{key: mcpFieldPairs, label: "Env", kind: "text", value: formatMCPPairs(pairs)},
		providerNewField{key: mcpFieldTools, label: "Tools", kind: "text", value: strings.Join(srv.Tools, ", ")},
		providerNewField{key: mcpFieldTimeout, label: "Timeout ms", kind: "text", value: timeout},
		providerNewField{key: mcpFieldSandbox, label: "Sandbox", kind: "cycle", opts: []string{"off", "on"}, value: sandbox},
		providerNewField{key: mcpFieldSecretKey, label: "Secret key", kind: "text", value: secretKey},
		providerNewField{key: mcpFieldSecret, label: "Secret value", kind: "masked"},
	)
	a.mcpState.form = f
	a.mcpState.mode = "form"
	a.mcpState.errorMsg = ""
	// Land on the first visible field.
	a.mcpState.form.sel = 0
	if !a.mcpFieldVisible(f.fields[0]) {
		a.mcpFormMove(1)
	}
}

// mcpCredKeys returns the keys the server's cred: values name, sorted.
func mcpCredKeys(s config.MCPServer) []string {
	seen := map[string]bool{}
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for _, v := range m {
			if kind, ref, ok := config.ParseMCPRef(v); ok && kind == config.MCPRefCred {
				seen[ref] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func formatMCPPairs(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ", ")
}

// parseMCPPairs reads NAME=value pairs separated by commas.
func parseMCPPairs(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%q is not NAME=value", part)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) mcpFormMove(dir int) {
	f := &a.mcpState.form
	for i := f.sel + dir; i >= 0 && i < len(f.fields); i += dir {
		if a.mcpFieldVisible(f.fields[i]) {
			f.sel = i
			return
		}
	}
}

func (a *App) handleMCPKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.mcpState
	if st.mode == "form" {
		return a.handleMCPFormKey(m)
	}
	rows := a.mcpRowsList()
	key := m.String()
	if key != "x" {
		st.confirm = ""
	}
	cur := mcpRow{}
	if st.selected < len(rows) {
		cur = rows[st.selected]
	}
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
	case "enter", " ":
		switch cur.kind {
		case "builtin":
			return a, a.changeMCPRow(cur.row.key)
		case "add":
			if key == "enter" {
				a.mcpFormFor("", true)
			}
		case "server":
			if key == "enter" {
				a.mcpFormFor(cur.server, false)
			} else {
				return a, a.mcpToggleServer(cur.server)
			}
		}
	case "r":
		if cur.kind == "server" {
			name, ctx := cur.server, a.ctx
			a.addSystem("mcp: restarting " + name + "…")
			return a, func() tea.Msg { return mcpDoneMsg{name: name, err: mcp.Active().Restart(ctx, name)} }
		}
	case "c", "x":
		switch {
		case cur.kind == "builtin" && key == "x":
			return a, a.unsetMCPRow(cur.row.key)
		case cur.kind == "server" && key == "x":
			if st.confirm != cur.server {
				st.confirm = cur.server
				return a, nil
			}
			st.confirm = ""
			if err := a.mcpDeleteServer(cur.server); err != nil {
				st.errorMsg = err.Error()
			} else {
				st.errorMsg = ""
				st.selected = min(st.selected, len(a.mcpRowsList())-1)
			}
		}
	}
	return a, nil
}

func (a *App) handleMCPFormKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.mcpState
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
		a.mcpFormMove(-1)
	case "down", "j":
		a.mcpFormMove(1)
	case "left", "right", " ":
		fld := &f.fields[f.sel]
		if fld.kind == "cycle" && len(fld.opts) > 0 {
			dir := 1
			if key == "left" {
				dir = -1
			}
			idx := indexOfString(fld.opts, fld.value)
			fld.value = fld.opts[(idx+dir+len(fld.opts))%len(fld.opts)]
			if fld.key == mcpFieldTransport {
				// The pairs field means env for stdio and headers for http.
				a.mcpFormMove(0)
			}
		}
	case "enter":
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
		return a, a.commitMCPForm()
	}
	return a, nil
}

// mcpServerFromForm builds the entry the form describes.
func (a *App) mcpServerFromForm() (config.MCPServer, error) {
	srv := config.MCPServer{Transport: a.mcpFormValue(mcpFieldTransport)}
	pairs, err := parseMCPPairs(a.mcpFormValue(mcpFieldPairs))
	if err != nil {
		return srv, err
	}
	if srv.Transport == "http" {
		srv.URL = a.mcpFormValue(mcpFieldURL)
		srv.Headers = pairs
	} else {
		srv.Transport = ""
		srv.Command = a.mcpFormValue(mcpFieldCommand)
		srv.Args = strings.Fields(a.mcpFormValue(mcpFieldArgs))
		srv.Env = pairs
		srv.Sandbox = a.mcpFormValue(mcpFieldSandbox) == "on"
	}
	srv.Tools = splitCSV(a.mcpFormValue(mcpFieldTools))
	if t := a.mcpFormValue(mcpFieldTimeout); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil {
			return srv, fmt.Errorf("timeout must be a number of milliseconds")
		}
		srv.TimeoutMS = n
	}
	// Keep what the form does not edit: the library's credential bindings and
	// the disabled flag of an existing entry.
	if !a.mcpState.form.isNew && a.settings.MCP != nil {
		old := a.settings.MCP.Servers[a.mcpState.form.name]
		srv.Disabled, srv.Secrets = old.Disabled, old.Secrets
	}
	return srv, nil
}

// commitMCPForm validates and saves the server to the global settings, stores a
// typed secret in the credential store, and starts it.
func (a *App) commitMCPForm() tea.Cmd {
	st := &a.mcpState
	f := st.form
	name := f.name
	if f.isNew {
		name = strings.TrimSpace(a.mcpFormValue(mcpFieldName))
	}
	srv, err := a.mcpServerFromForm()
	if err != nil {
		st.errorMsg = err.Error()
		return nil
	}
	if err := config.ValidateMCPServer(name, srv, true); err != nil {
		st.errorMsg = err.Error()
		return nil
	}
	if f.isNew && a.settings.MCP != nil {
		if _, exists := a.settings.MCP.Servers[name]; exists {
			st.errorMsg = "a server named " + name + " already exists"
			return nil
		}
	}
	secretKey, secret := a.mcpFormValue(mcpFieldSecretKey), a.mcpFormValue(mcpFieldSecret)
	if secret != "" {
		if secretKey == "" || !config.ValidMCPRefName(secretKey) {
			st.errorMsg = "enter a secret key (letters, digits and _) to store the secret under"
			return nil
		}
		if a.resolver == nil {
			st.errorMsg = "the credential store is not available in this session"
			return nil
		}
	}
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.MCP == nil {
			s.MCP = &config.MCPSettings{}
		}
		if s.MCP.Servers == nil {
			s.MCP.Servers = map[string]config.MCPServer{}
		}
		s.MCP.Servers[name] = srv
		return nil
	}); err != nil {
		st.errorMsg = err.Error()
		return nil
	}
	if secret != "" {
		if err := a.resolver.StoreMCPSecret(name, secretKey, secret); err != nil {
			st.errorMsg = "saved, but the secret could not be stored: " + err.Error()
			return nil
		}
	}
	_ = a.reloadSettings()
	st.mode, st.errorMsg = "", ""
	a.mcpApplyServer(name, srv)
	a.addSystem("mcp: saved " + name)
	return nil
}

// mcpApplyServer connects (or disconnects) one server in this process and makes
// the next session see its tools.
func (a *App) mcpApplyServer(name string, srv config.MCPServer) {
	if m := mcp.Active(); m != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := m.Upsert(ctx, name, srv); err != nil {
			a.mcpState.errorMsg = name + ": " + mcpClean(err.Error(), 160)
		}
	}
	a.invalidateAgentSession()
}

func (a *App) mcpToggleServer(name string) tea.Cmd {
	if a.settings.MCP == nil {
		return nil
	}
	srv, ok := a.settings.MCP.Servers[name]
	if !ok {
		return nil
	}
	srv.Disabled = !srv.Disabled
	if err := a.mutateGlobalSetting(func(s *config.Settings) {
		if s.MCP != nil && s.MCP.Servers != nil {
			s.MCP.Servers[name] = srv
		}
	}); err != nil {
		a.mcpState.errorMsg = err.Error()
		return nil
	}
	a.mcpApplyServer(name, srv)
	return nil
}

// mcpDeleteServer removes a server, stops it and clears the secrets its cred:
// values named.
func (a *App) mcpDeleteServer(name string) error {
	var keys []string
	if a.settings.MCP != nil {
		keys = mcpCredKeys(a.settings.MCP.Servers[name])
	}
	if err := a.mutateGlobalSetting(func(s *config.Settings) {
		if s.MCP != nil {
			delete(s.MCP.Servers, name)
			if len(s.MCP.Servers) == 0 {
				s.MCP.Servers = nil
			}
		}
	}); err != nil {
		return err
	}
	if a.resolver != nil {
		for _, k := range keys {
			a.resolver.ClearMCPSecret(name, k)
		}
	}
	mcp.Active().Remove(name)
	a.invalidateAgentSession()
	a.addSystem("mcp: removed " + name)
	return nil
}

// mcpStatus is the screen switcher's status text.
func (a *App) mcpStatus() string {
	n := 0
	for _, s := range mcp.Active().Status() {
		if s.State == mcp.StateRunning {
			n++
		}
	}
	return fmt.Sprintf("%d running", n)
}
