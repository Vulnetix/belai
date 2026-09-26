package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
	"github.com/vulnetix/belai/internal/vulnetixenroll"
)

// The Getting started view runs once per user on the first interactive
// launch, and again on /vulnetix setup. It explains the main keys and
// commands, then walks the Vulnetix setup: install the CLI through the
// platform package manager (only after the user picks Install), create an
// account through the enrollment flow, log the CLI in with the device flow,
// and turn the AI Firewall and the Vulnetix MCP server on.
//
// The sign-up password lives only in this view's form until it is posted to
// auth.vulnetix.com; it is cleared as soon as the post returns, whenever the
// form is left, and never reaches the transcript, a log or a model.

type gsStep int

const (
	gsKeys gsStep = iota
	gsCommands
	gsCLI
	gsAccount
	gsSignup
	gsLogin
	gsEnable
)

// gsKeyList and gsCommandList are what the first two pages teach.
var (
	gsKeyList     = []string{"tab", "shift+tab", "f3", "f4", "f9"}
	gsCommandList = []string{"permissions", "model", "settings", "help"}
)

type gsField struct {
	key    string
	label  string
	masked bool
	value  string
}

type gettingStartedState struct {
	step gsStep
	sel  int

	// CLI step.
	cliChecked bool
	cliPath    string
	plan       vulnetixcli.InstallPlan
	planOK     bool
	installing bool
	installLog []string
	installErr string
	installCh  chan string

	// Account step.
	credChecked bool
	hasCred     bool

	// Sign-up step.
	fields     []gsField
	editing    bool
	fieldErrs  map[string]string
	submitting bool
	signupMsg  string
	signupErr  string
	signupURL  string
	signedUp   bool
	enroll     *vulnetixenroll.Client

	// Login step.
	grant       *vulnetixcli.DeviceGrant
	loginBusy   bool
	loginErr    string
	loginCancel context.CancelFunc

	// Enable step.
	enabling  bool
	enableLog []string
	enabled   bool
}

// Messages.
type (
	gsCLIProbeMsg struct {
		path   string
		plan   vulnetixcli.InstallPlan
		planOK bool
	}
	gsInstallLineMsg struct{ line string }
	gsInstallDoneMsg struct{ err error }
	gsCredMsg        struct{ ok bool }
	gsEnrollMsg      struct {
		res vulnetixenroll.Result
		err error
	}
	gsGrantMsg struct {
		grant vulnetixcli.DeviceGrant
		err   error
	}
	gsLoginDoneMsg  struct{ err error }
	gsEnableDoneMsg struct{ lines []string }
)

// maybeStartGettingStarted opens the view on a first interactive launch.
// Start calls it; New alone (every test) never does.
func (a *App) maybeStartGettingStarted(opts Options) {
	if a.state.OnboardedAt != "" || opts.Prompt != "" || opts.ResumeSession != "" {
		return
	}
	a.gettingStartedOnInit = true
}

// openGettingStarted opens the view at its first page.
func (a *App) openGettingStarted() tea.Cmd {
	a.gsState = gettingStartedState{}
	return a.push(viewGettingStarted)
}

func (a *App) enterGettingStarted() tea.Cmd { return nil }

// finishGettingStarted records that the user has been through the view and
// returns to chat. The form's secrets are dropped with the state.
func (a *App) finishGettingStarted() {
	a.gsClearSecrets()
	if a.gsState.loginCancel != nil {
		a.gsState.loginCancel()
	}
	a.gsState = gettingStartedState{}
	a.state.OnboardedAt = time.Now().UTC().Format(time.RFC3339)
	_ = config.SaveState(a.state)
	a.popToChat()
}

func (a *App) gsClearSecrets() {
	for i := range a.gsState.fields {
		if a.gsState.fields[i].masked {
			a.gsState.fields[i].value = ""
		}
	}
	a.editor.Reset()
	a.editor.Masked = false
	a.gsState.editing = false
}

// gsGo moves to step and starts whatever that step does on arrival.
func (a *App) gsGo(step gsStep) tea.Cmd {
	st := &a.gsState
	if st.step == gsSignup && step != gsSignup {
		a.gsClearSecrets()
	}
	st.step, st.sel = step, 0
	switch step {
	case gsCLI:
		if !st.cliChecked {
			return gsProbeCLI
		}
	case gsAccount:
		if !st.credChecked {
			return gsCheckCred(a.workdir)
		}
	case gsSignup:
		if st.fields == nil {
			st.fields = []gsField{
				{key: "email", label: "Email"},
				{key: "company", label: "Company (optional)"},
				{key: "password", label: "Password", masked: true},
				{key: "password_repeat", label: "Repeat password", masked: true},
			}
		}
	case gsLogin:
		return a.gsStartLogin()
	case gsEnable:
		return a.gsEnable()
	}
	return nil
}

func gsProbeCLI() tea.Msg {
	var m gsCLIProbeMsg
	if cli, err := vulnetixcli.Detect(); err == nil {
		m.path = cli.Path
		return m
	}
	m.plan, m.planOK = vulnetixcli.PlanInstall("", nil)
	return m
}

func gsCheckCred(workdir string) tea.Cmd {
	return func() tea.Msg {
		_, err := credentials.VulnetixAuthHeader(workdir)
		return gsCredMsg{ok: err == nil}
	}
}

// gsStartInstall runs the confirmed install plan, streaming its output.
func (a *App) gsStartInstall() tea.Cmd {
	st := &a.gsState
	st.installing, st.installErr, st.installLog = true, "", nil
	ch := make(chan string, 64)
	st.installCh = ch
	plan, ctx := st.plan, a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	run := func() tea.Msg {
		err := vulnetixcli.RunInstall(ctx, plan, func(line string) {
			select {
			case ch <- line:
			default:
			}
		})
		close(ch)
		return gsInstallDoneMsg{err: err}
	}
	return tea.Batch(run, gsNextLine(ch))
}

func gsNextLine(ch chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return gsInstallLineMsg{line: line}
	}
}

// gsSubmitSignup posts the form. The password is copied into the request
// and cleared from the form at once.
func (a *App) gsSubmitSignup() tea.Cmd {
	st := &a.gsState
	form := vulnetixenroll.Form{}
	for _, f := range st.fields {
		switch f.key {
		case "email":
			form.Email = f.value
		case "company":
			form.Company = f.value
		case "password":
			form.Password = f.value
		case "password_repeat":
			form.PasswordRepeat = f.value
		}
	}
	a.gsClearSecrets()
	st.submitting, st.fieldErrs, st.signupErr, st.signupMsg = true, nil, "", ""
	if st.enroll == nil {
		st.enroll = vulnetixenroll.New()
	}
	c, ctx := st.enroll, a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		defer form.Clear()
		if err := c.Begin(ctx); err != nil {
			return gsEnrollMsg{err: err}
		}
		res, err := c.Submit(ctx, form)
		return gsEnrollMsg{res: res, err: err}
	}
}

// gsStartLogin starts the device grant; the poll follows its result.
func (a *App) gsStartLogin() tea.Cmd {
	st := &a.gsState
	st.grant, st.loginErr = nil, ""
	if st.cliPath == "" {
		if cli, err := vulnetixcli.Detect(); err == nil {
			st.cliPath = cli.Path
		}
	}
	if st.cliPath == "" {
		st.loginErr = "the Vulnetix CLI is needed to store the login · install it, then run /vulnetix setup"
		return nil
	}
	st.loginBusy = true
	ctx, cancel := context.WithCancel(a.baseCtx())
	st.loginCancel = cancel
	return func() tea.Msg {
		g, err := vulnetixcli.DeviceLogin{}.Start(ctx)
		return gsGrantMsg{grant: g, err: err}
	}
}

func (a *App) baseCtx() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (a *App) gsPoll(g vulnetixcli.DeviceGrant) tea.Cmd {
	st := &a.gsState
	path := st.cliPath
	ctx, cancel := context.WithCancel(a.baseCtx())
	if st.loginCancel != nil {
		st.loginCancel()
	}
	st.loginCancel = cancel
	return func() tea.Msg {
		org, key, err := vulnetixcli.DeviceLogin{}.Poll(ctx, g)
		if err != nil {
			return gsLoginDoneMsg{err: err}
		}
		return gsLoginDoneMsg{err: vulnetixcli.CLI{Path: path}.SaveLogin(ctx, org, key)}
	}
}

// gsEnable turns the AI Firewall on in the global settings and installs the
// Vulnetix MCP server, then pushes the configured provider keys.
func (a *App) gsEnable() tea.Cmd {
	st := &a.gsState
	st.enabling, st.enableLog = true, nil
	var lines []string
	on := true
	if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
		if s.Vulnetix == nil {
			s.Vulnetix = &config.VulnetixSettings{}
		}
		s.Vulnetix.FirewallEnabled = &on
		return nil
	}); err != nil {
		lines = append(lines, "✗ AI Firewall: "+err.Error())
	} else {
		_ = a.reloadSettings()
		if a.resolver != nil {
			a.resolver.RefreshVulnetixCred()
		}
		if cfg, err := a.resolveConfig(); err == nil {
			a.cfg = cfg
		}
		a.refreshFooter()
		if !a.firewallEnabled() {
			lines = append(lines, "• AI Firewall: on in global settings, but "+a.firewallOffReason())
		} else {
			lines = append(lines, "✓ AI Firewall on · F10 turns it off for this project")
		}
	}
	ctx, wd := a.baseCtx(), a.workdir
	install := func() tea.Msg {
		n, err := installVulnetixMCP(ctx, wd)
		if err != nil {
			return gsEnableDoneMsg{lines: append(lines, "✗ Vulnetix MCP: "+mcpClean(err.Error(), 200))}
		}
		return gsEnableDoneMsg{lines: append(lines, fmt.Sprintf("✓ Vulnetix MCP connected · %d tools · /vulnetix mcp remove to undo", n))}
	}
	return tea.Batch(install, a.syncAllFirewallKeys())
}

// firewallOffReason names what keeps the firewall off after it was enabled.
func (a *App) firewallOffReason() string {
	if src := string(a.eff.Origin["firewall_enabled"]); src != "" {
		return src + " turns it off here"
	}
	return "it is off for this session"
}

// handleGettingStartedMsg applies one of the view's async results. It
// returns handled=false for messages that are not the view's.
func (a *App) handleGettingStartedMsg(msg tea.Msg) (tea.Cmd, bool) {
	st := &a.gsState
	switch m := msg.(type) {
	case gsCLIProbeMsg:
		st.cliChecked, st.cliPath, st.plan, st.planOK = true, m.path, m.plan, m.planOK
		if m.planOK {
			st.sel = 1 // Skip is the default; Install is an explicit choice.
		}
	case gsInstallLineMsg:
		st.installLog = append(st.installLog, m.line)
		if len(st.installLog) > 8 {
			st.installLog = st.installLog[len(st.installLog)-8:]
		}
		return gsNextLine(st.installCh), true
	case gsInstallDoneMsg:
		st.installing = false
		if m.err != nil {
			st.installErr = m.err.Error()
			return nil, true
		}
		if cli, err := vulnetixcli.Detect(); err == nil {
			st.cliPath = cli.Path
		} else {
			st.installErr = "installed, but vulnetix is not on PATH yet · open a new shell, then /vulnetix setup"
		}
	case gsCredMsg:
		st.credChecked, st.hasCred = true, m.ok
		if m.ok {
			st.sel = 0
		}
	case gsEnrollMsg:
		st.submitting = false
		if m.err != nil {
			st.signupErr = m.err.Error()
			return nil, true
		}
		switch m.res.Outcome {
		case vulnetixenroll.Invalid:
			st.fieldErrs = m.res.Errors
			if msg, ok := m.res.Errors[""]; ok {
				st.signupErr = msg
			}
		case vulnetixenroll.Denied:
			st.signupErr = m.res.Message
		case vulnetixenroll.Browser:
			st.signupMsg, st.signupURL = m.res.Message, m.res.URL
		default:
			st.signedUp, st.signupMsg = true, m.res.Message
		}
	case gsGrantMsg:
		if m.err != nil {
			st.loginBusy, st.loginErr = false, m.err.Error()
			return nil, true
		}
		st.grant = &m.grant
		_ = vulnetixcli.OpenBrowser(m.grant.BrowseURL())
		return a.gsPoll(m.grant), true
	case gsLoginDoneMsg:
		st.loginBusy = false
		if m.err != nil {
			if !errors.Is(m.err, context.Canceled) {
				st.loginErr = m.err.Error()
			}
			return nil, true
		}
		st.hasCred = true
		if a.resolver != nil {
			a.resolver.RefreshVulnetixCred()
		}
		return a.gsGo(gsEnable), true
	case gsEnableDoneMsg:
		st.enabling, st.enabled = false, true
		st.enableLog = append(st.enableLog, m.lines...)
		a.handleVulnetixMCPDoneQuiet()
	default:
		return nil, false
	}
	return nil, true
}

// handleVulnetixMCPDoneQuiet refreshes the session after the MCP install
// without the chat line /vulnetix mcp prints; the view shows the outcome.
func (a *App) handleVulnetixMCPDoneQuiet() {
	_ = a.reloadSettings()
	a.invalidateAgentSession()
}

// gsOptions lists the choices on the steps that have them.
func (a *App) gsOptions() []string {
	st := &a.gsState
	switch st.step {
	case gsCLI:
		if st.cliPath == "" && st.planOK && !st.installing {
			return []string{"Install with " + string(st.plan.Manager), "Skip"}
		}
	case gsAccount:
		if st.credChecked && !st.hasCred {
			return []string{"Create a Vulnetix account", "I already have one — log in", "Skip"}
		}
	}
	return nil
}

func (a *App) handleGettingStartedKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.gsState
	key := m.String()

	if st.step == gsSignup && st.editing {
		switch key {
		case "esc":
			a.editor.Reset()
			a.editor.Masked = false
			st.editing = false
			return a, nil
		case "enter":
			if st.sel < len(st.fields) {
				st.fields[st.sel].value = a.editor.Value()
			}
			a.editor.Reset()
			a.editor.Masked = false
			st.editing = false
			if st.sel < len(st.fields) {
				st.sel++
			}
			return a, nil
		default:
			return a, a.editor.Update(m)
		}
	}

	opts := a.gsOptions()
	switch key {
	case "up", "k":
		if st.sel > 0 {
			st.sel--
		}
		return a, nil
	case "down", "j":
		limit := len(opts) - 1
		if st.step == gsSignup {
			limit = len(st.fields) // the last row is the submit button
		}
		if st.sel < limit {
			st.sel++
		}
		return a, nil
	case "o":
		if st.step == gsSignup && st.signupURL != "" {
			_ = vulnetixcli.OpenBrowser(st.signupURL)
		}
		if st.step == gsLogin && st.grant != nil {
			_ = vulnetixcli.OpenBrowser(st.grant.BrowseURL())
		}
		return a, nil
	case "esc":
		return a, a.gsBack()
	case "left":
		if st.step == gsKeys || st.step == gsCommands {
			return a, a.gsBack()
		}
		return a, nil
	case "right":
		if st.step == gsKeys || st.step == gsCommands {
			return a, a.gsGo(st.step + 1)
		}
		return a, nil
	case "enter", " ":
		return a, a.gsEnter(opts)
	}
	return a, nil
}

// gsBack steps back a page; on the first page it skips the whole view.
func (a *App) gsBack() tea.Cmd {
	st := &a.gsState
	switch st.step {
	case gsKeys:
		a.finishGettingStarted()
		return nil
	case gsCLI:
		if st.installing {
			return nil
		}
	case gsSignup:
		if st.submitting {
			return nil
		}
		return a.gsGo(gsAccount)
	case gsLogin:
		if st.loginCancel != nil {
			st.loginCancel()
		}
		st.loginBusy, st.grant = false, nil
		return a.gsGo(gsAccount)
	case gsEnable:
		if st.enabling {
			return nil
		}
		a.finishGettingStarted()
		return nil
	}
	return a.gsGo(st.step - 1)
}

func (a *App) gsEnter(opts []string) tea.Cmd {
	st := &a.gsState
	switch st.step {
	case gsKeys, gsCommands:
		return a.gsGo(st.step + 1)
	case gsCLI:
		switch {
		case !st.cliChecked || st.installing:
			return nil
		case len(opts) > 0 && st.sel == 0:
			return a.gsStartInstall()
		default:
			return a.gsGo(gsAccount)
		}
	case gsAccount:
		if !st.credChecked {
			return nil
		}
		if st.hasCred {
			return a.gsGo(gsEnable)
		}
		switch st.sel {
		case 0:
			return a.gsGo(gsSignup)
		case 1:
			return a.gsGo(gsLogin)
		default:
			a.finishGettingStarted()
			return nil
		}
	case gsSignup:
		if st.submitting {
			return nil
		}
		if st.signedUp || st.signupURL != "" {
			return a.gsGo(gsLogin)
		}
		if st.sel < len(st.fields) {
			f := st.fields[st.sel]
			a.editor.Reset()
			a.editor.Masked = f.masked
			if !f.masked {
				a.editor.SetValue(f.value)
				a.editor.CursorEnd()
			}
			_ = a.editor.Focus()
			st.editing = true
			return nil
		}
		return a.gsSubmitSignup()
	case gsLogin:
		if st.loginErr != "" && !st.loginBusy {
			if st.cliPath == "" {
				a.finishGettingStarted()
				return nil
			}
			return a.gsStartLogin()
		}
	case gsEnable:
		if st.enabled {
			a.finishGettingStarted()
		}
	}
	return nil
}

// gettingStartedView renders the current page.
func (a *App) gettingStartedView() string {
	w := a.contentWidth()
	st := &a.gsState
	titles := map[gsStep]string{
		gsKeys: "Keys", gsCommands: "Commands", gsCLI: "Vulnetix CLI", gsAccount: "Vulnetix account",
		gsSignup: "Create an account", gsLogin: "Log in", gsEnable: "AI Firewall and MCP",
	}
	var b strings.Builder
	b.WriteString(components.SectionHeader("Getting started · "+titles[st.step], fmt.Sprintf("%d/5", gsPage(st.step)), w))
	muted, emph, accent := components.MutedStyle, components.EmphStyle, components.AccentStyle
	line := func(s string) { b.WriteString(s + "\n") }
	help := components.HelpBar("enter", "next", "esc", "back")

	switch st.step {
	case gsKeys:
		line(muted.Render("Welcome to Belai. A few keys worth knowing:"))
		line("")
		for _, k := range gsKeyList {
			line(fmt.Sprintf("  %s  %s", components.KeyStyle.Render(fmt.Sprintf("%-10s", k)), keyDescription(k)))
		}
		line("")
		line(muted.Render("Every binding is listed under /help."))
		help = components.HelpBar("enter/→", "next", "esc", "skip getting started")
	case gsCommands:
		line(muted.Render("Type / in the prompt for every command. The ones to start with:"))
		line("")
		for _, name := range gsCommandList {
			desc := ""
			if a.registry != nil {
				if c, ok := a.registry.Command(name); ok {
					desc = c.Description
				}
			}
			line(fmt.Sprintf("  %s  %s", components.KeyStyle.Render(fmt.Sprintf("%-13s", "/"+name)), desc))
		}
		help = components.HelpBar("enter/→", "next", "←/esc", "back")
	case gsCLI:
		switch {
		case !st.cliChecked:
			line(muted.Render("Looking for the vulnetix CLI…"))
		case st.cliPath != "":
			line(accent.Render("✓ ") + "vulnetix CLI found at " + emph.Render(st.cliPath))
		case st.installing:
			line("Installing: " + emph.Render(st.plan.Commands()))
			line("")
			for _, l := range st.installLog {
				line(muted.Render("  " + mcpClean(l, w-4)))
			}
			help = muted.Render("installing…")
		case st.planOK:
			line("The vulnetix CLI is not installed. Belai can install it with:")
			line("")
			line("  " + emph.Render(st.plan.Commands()))
			line("")
		default:
			line("The vulnetix CLI is not installed, and neither Homebrew nor Scoop was found. Install it with:")
			line("")
			line("  " + emph.Render(vulnetixcli.ManualInstallHint))
			line("")
			line(muted.Render("then run /vulnetix setup."))
		}
		if st.installErr != "" {
			line("")
			line(components.DangerStyle.Render("✗ " + mcpClean(st.installErr, 300)))
		}
	case gsAccount:
		switch {
		case !st.credChecked:
			line(muted.Render("Checking for Vulnetix CLI credentials…"))
		case st.hasCred:
			line(accent.Render("✓ ") + "Vulnetix CLI credentials found.")
			help = components.HelpBar("enter", "turn on the AI Firewall and MCP", "esc", "back")
		default:
			line("A Vulnetix account turns on the AI Firewall and the Vulnetix MCP server.")
		}
	case gsSignup:
		line(muted.Render("Sent only to auth.vulnetix.com. The password is never stored or shown to a model."))
		line("")
		if st.editing {
			f := st.fields[st.sel]
			line(a.renderFieldEditor(f.label, w))
			help = components.HelpBar("enter", "save", "esc", "cancel")
			break
		}
		for i, f := range st.fields {
			val := f.value
			if f.masked && val != "" {
				val = strings.Repeat("•", 8)
			}
			label := fmt.Sprintf("%-20s", f.label)
			if i == st.sel {
				label = accent.Bold(true).Render(label)
			} else {
				label = muted.Render(label)
			}
			row := components.Cursor(i == st.sel) + label + "  " + val
			if e := st.fieldErrs[f.key]; e != "" {
				row += "  " + components.DangerStyle.Render("✗ "+e)
			}
			line(row)
		}
		submit := "[ Create account ]"
		if st.submitting {
			submit = "[ Creating… ]"
		}
		if st.sel == len(st.fields) {
			submit = emph.Render(submit)
		}
		line(components.Cursor(st.sel == len(st.fields)) + submit)
		if st.signupErr != "" {
			line("")
			line(components.DangerStyle.Render("✗ " + st.signupErr))
		}
		if st.signupMsg != "" {
			line("")
			line(accent.Render("✓ ") + st.signupMsg)
			if st.signupURL != "" {
				line(muted.Render("  " + st.signupURL + " · o opens it"))
			}
			help = components.HelpBar("enter", "log in", "esc", "back")
		} else {
			help = components.HelpBar("↑↓", "move", "enter", "edit·submit", "esc", "back")
		}
	case gsLogin:
		switch {
		case st.grant != nil && st.loginBusy:
			line("Approve this login in your browser:")
			line("")
			line("  " + emph.Render(st.grant.VerificationURI))
			line("")
			line("Check the code matches:  " + components.KeyStyle.Render(st.grant.UserCode))
			line("")
			line(muted.Render("Waiting for approval… (o opens the page again)"))
			help = components.HelpBar("o", "open browser", "esc", "cancel")
		case st.loginBusy:
			line(muted.Render("Starting the Vulnetix login…"))
		case st.loginErr != "":
			line(components.DangerStyle.Render("✗ " + mcpClean(st.loginErr, 300)))
			if st.cliPath != "" {
				help = components.HelpBar("enter", "try again", "esc", "back")
			} else {
				help = components.HelpBar("enter", "finish", "esc", "back")
			}
		}
	case gsEnable:
		if st.enabling {
			line(muted.Render("Turning on the AI Firewall and connecting the Vulnetix MCP server…"))
		}
		for _, l := range st.enableLog {
			line(l)
		}
		if st.enabled {
			line("")
			line(muted.Render("Provider keys you save in /providers are stored with the AI Firewall for your org, in the background."))
			help = components.HelpBar("enter", "start using Belai")
		} else {
			help = ""
		}
	}

	if opts := a.gsOptions(); len(opts) > 0 {
		line("")
		for i, o := range opts {
			label := o
			if i == st.sel {
				label = accent.Bold(true).Render(o)
			}
			line(components.Cursor(i == st.sel) + label)
		}
		help = components.HelpBar("↑↓", "choose", "enter", "confirm", "esc", "back")
	}
	if help != "" {
		b.WriteString("\n" + help + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// gsPage maps a step to the page count shown in the header.
func gsPage(s gsStep) int {
	switch s {
	case gsKeys:
		return 1
	case gsCommands:
		return 2
	case gsCLI:
		return 3
	case gsAccount, gsSignup, gsLogin:
		return 4
	}
	return 5
}

// keyDescription reads a binding's description from the /help table, so the
// getting-started page cannot drift from it.
func keyDescription(key string) string {
	for _, sec := range keySections() {
		if sec.Title != "anywhere" && sec.Title != "chat" {
			continue
		}
		for _, kb := range sec.Bindings {
			if kb.Keys == key {
				return kb.Desc
			}
		}
	}
	return ""
}
