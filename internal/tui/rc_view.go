package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// The remote control view (/rc) is a Getting started for `belai rc`: what
// remote control is, a live checklist of what it needs (the Vulnetix CLI, a
// browser login, sync, guardrails, a directory to offer) with a step for each
// gap, then the daemon's status with the Sessions page one key away. The
// daemon itself runs detached (`belai rc --detach`), so it outlives this TUI.

type rcPage int

const (
	rcIntro rcPage = iota
	rcChecklist
	rcStatus
)

type rcViewState struct {
	page rcPage
	sel  int

	checks   rc.Checks
	checking bool

	grant       *vulnetixcli.DeviceGrant
	loginBusy   bool
	loginErr    string
	loginCancel context.CancelFunc

	// extraDirs are offered with --dir when the daemon starts: this
	// directory, when the user asked for it and it is not offered already.
	extraDirs []string

	starting bool
	stopping bool
	note     string
	noteErr  bool
}

type (
	rcChecksMsg struct{ checks rc.Checks }
	rcGrantMsg  struct {
		grant vulnetixcli.DeviceGrant
		err   error
	}
	rcLoginDoneMsg struct{ err error }
	rcStartDoneMsg struct {
		out string
		err error
	}
	rcStopDoneMsg struct{ err error }
)

// rcAction is one choice on the checklist page.
type rcAction int

const (
	rcActInstall rcAction = iota
	rcActLogin
	rcActSync
	rcActDir
	rcActStart
	rcActRecheck
)

type rcOption struct {
	label string
	act   rcAction
}

// rcURL is the Sessions page on the configured origin.
func rcURL() string { return rc.ManageURL(os.Getenv("VULNETIX_WEB_URL")) }

// openRC opens /rc: the status page while the daemon runs, else the intro.
func (a *App) openRC() tea.Cmd {
	if a.rcState.loginCancel != nil {
		a.rcState.loginCancel()
	}
	a.rcState = rcViewState{}
	if _, live := rc.ReadRecord(); live {
		a.rcState.page = rcStatus
	}
	return tea.Batch(a.push(viewRC), a.rcCheck())
}

func (a *App) enterRC() tea.Cmd { return nil }

// rcCheck runs the preflight off the UI goroutine. It never calls the
// website: the daemon's own preflight does that when it starts.
func (a *App) rcCheck() tea.Cmd {
	a.rcState.checking = true
	wd, extra := a.workdir, append([]string(nil), a.rcState.extraDirs...)
	return func() tea.Msg {
		settings, err := config.LoadMerged(wd)
		if err != nil {
			return rcChecksMsg{checks: rc.Checks{{Name: "Settings", Level: rc.Fail, Detail: err.Error()}}}
		}
		offered, _ := rc.Collect(extra, wd)
		return rcChecksMsg{checks: rc.Preflight(context.Background(), rc.PreflightOptions{
			Workdir: wd, Settings: settings, Dirs: offered,
		})}
	}
}

// rcOptions lists the checklist's actions: one per fixable gap, then start
// (only when nothing fails) and check again.
func (a *App) rcOptions() []rcOption {
	st := &a.rcState
	if st.page != rcChecklist || st.checking || st.loginBusy {
		return nil
	}
	var out []rcOption
	for _, c := range st.checks {
		if c.Level != rc.Fail {
			continue
		}
		switch c.Name {
		case rc.CheckCLI:
			out = append(out, rcOption{"Install the Vulnetix CLI (Getting started)", rcActInstall})
		case rc.CheckCredential:
			if _, ok := st.checks.Find(rc.CheckCLI); ok {
				out = append(out, rcOption{"Log in to Vulnetix in your browser", rcActLogin})
			}
		case rc.CheckSync:
			out = append(out, rcOption{"Turn session sync on", rcActSync})
		case rc.CheckDirs:
			out = append(out, rcOption{"Offer this directory (" + a.workdir + ")", rcActDir})
		}
	}
	if st.checks.OK() {
		out = append(out, rcOption{"Start remote control", rcActStart})
	}
	return append(out, rcOption{"Check again", rcActRecheck})
}

// rcStartLogin runs the Vulnetix device login, as Getting started does.
func (a *App) rcStartLogin() tea.Cmd {
	st := &a.rcState
	st.grant, st.loginErr = nil, ""
	st.loginBusy = true
	ctx, cancel := context.WithCancel(a.baseCtx())
	st.loginCancel = cancel
	return func() tea.Msg {
		g, err := vulnetixcli.DeviceLogin{}.Start(ctx)
		return rcGrantMsg{grant: g, err: err}
	}
}

func (a *App) rcPoll(g vulnetixcli.DeviceGrant) tea.Cmd {
	ctx := a.baseCtx()
	if a.rcState.loginCancel != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		prev := a.rcState.loginCancel
		a.rcState.loginCancel = func() { prev(); cancel() }
	}
	return func() tea.Msg {
		cli, err := vulnetixcli.Detect()
		if err != nil {
			return rcLoginDoneMsg{err: err}
		}
		org, key, err := vulnetixcli.DeviceLogin{}.Poll(ctx, g)
		if err != nil {
			return rcLoginDoneMsg{err: err}
		}
		return rcLoginDoneMsg{err: cli.SaveLogin(ctx, org, key)}
	}
}

// rcStart starts the daemon detached, offering any extra directories, and
// reports what it printed.
func (a *App) rcStart() tea.Cmd {
	st := &a.rcState
	st.starting, st.note, st.noteErr = true, "", false
	st.page = rcStatus
	wd, extra := a.workdir, append([]string(nil), st.extraDirs...)
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return rcStartDoneMsg{err: err}
		}
		args := []string{"rc", "--detach"}
		for _, d := range extra {
			args = append(args, "--dir", d)
		}
		cmd := exec.Command(exe, args...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		return rcStartDoneMsg{out: string(out), err: err}
	}
}

func rcStopCmd() tea.Cmd {
	return func() tea.Msg {
		_, err := rc.Stop(15 * time.Second)
		return rcStopDoneMsg{err: err}
	}
}

// handleRCMsg applies the view's async results; handled=false otherwise.
func (a *App) handleRCMsg(msg tea.Msg) (tea.Cmd, bool) {
	st := &a.rcState
	switch m := msg.(type) {
	case rcChecksMsg:
		st.checking, st.checks, st.sel = false, m.checks, 0
	case rcGrantMsg:
		if m.err != nil {
			st.loginBusy, st.loginErr = false, m.err.Error()
			return nil, true
		}
		st.grant = &m.grant
		_ = vulnetixcli.OpenBrowser(m.grant.BrowseURL())
		return a.rcPoll(m.grant), true
	case rcLoginDoneMsg:
		st.loginBusy, st.grant = false, nil
		if m.err != nil {
			if !errors.Is(m.err, context.Canceled) {
				st.loginErr = m.err.Error()
			}
			return nil, true
		}
		if a.resolver != nil {
			a.resolver.RefreshVulnetixCred()
		}
		return tea.Batch(a.rcCheck(), a.retrySessionSync()), true
	case rcStartDoneMsg:
		st.starting = false
		st.note, st.noteErr = strings.TrimSpace(m.out), m.err != nil
		if m.err != nil && st.note == "" {
			st.note = m.err.Error()
		}
		a.pollRC(true)
	case rcStopDoneMsg:
		st.stopping = false
		if m.err != nil {
			st.note, st.noteErr = m.err.Error(), true
		} else {
			st.note, st.noteErr = "Remote control stopped. Its sessions ended and moved to History.", false
		}
		a.pollRC(true)
	default:
		return nil, false
	}
	return nil, true
}

func (a *App) handleRCKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.rcState
	opts := a.rcOptions()
	switch m.String() {
	case "esc":
		if st.loginBusy {
			if st.loginCancel != nil {
				st.loginCancel()
			}
			st.loginBusy, st.grant = false, nil
			return a, nil
		}
		if st.page == rcChecklist {
			st.page = rcIntro
			return a, nil
		}
		a.pop()
		return a, nil
	case "up", "k":
		if st.sel > 0 {
			st.sel--
		}
	case "down", "j":
		if st.sel < len(opts)-1 {
			st.sel++
		}
	case "o", "ctrl+y":
		if st.loginBusy && st.grant != nil {
			_ = vulnetixcli.OpenBrowser(st.grant.BrowseURL())
			return a, nil
		}
		return a, openLink(rcURL())
	case "r":
		st.note = ""
		a.pollRC(true)
		return a, a.rcCheck()
	case "s":
		if _, live := rc.ReadRecord(); live && !st.stopping {
			st.stopping = true
			return a, rcStopCmd()
		}
	case "enter", " ":
		switch st.page {
		case rcIntro:
			st.page, st.sel = rcChecklist, 0
			return a, a.rcCheck()
		case rcStatus:
			if _, live := rc.ReadRecord(); !live && !st.starting {
				st.page, st.sel = rcChecklist, 0
				return a, a.rcCheck()
			}
			return a, nil
		}
		if st.sel >= len(opts) {
			return a, nil
		}
		switch opts[st.sel].act {
		case rcActInstall:
			cmd := a.openGettingStarted()
			return a, tea.Batch(cmd, a.gsGo(gsCLI))
		case rcActLogin:
			return a, a.rcStartLogin()
		case rcActSync:
			t := true
			if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
				if s.Sync == nil {
					s.Sync = &config.SyncSettings{}
				}
				s.Sync.Enabled = &t
				return nil
			}); err != nil {
				st.note, st.noteErr = err.Error(), true
				return a, nil
			}
			_ = a.reloadSettings()
			return a, tea.Batch(a.rcCheck(), a.retrySessionSync())
		case rcActDir:
			st.extraDirs = append(st.extraDirs, a.workdir)
			return a, a.rcCheck()
		case rcActStart:
			return a, a.rcStart()
		case rcActRecheck:
			return a, a.rcCheck()
		}
	}
	return a, nil
}

// rcView renders the current page.
func (a *App) rcView() string {
	w := a.contentWidth()
	st := &a.rcState
	muted, emph, accent := components.MutedStyle, components.EmphStyle, components.AccentStyle
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	titles := map[rcPage]string{rcIntro: "What it is", rcChecklist: "Setup", rcStatus: "Status"}
	b.WriteString(components.SectionHeader("Remote control · "+titles[st.page], fmt.Sprintf("%d/3", int(st.page)+1), w))
	url := rcURL()
	help := ""

	switch st.page {
	case rcIntro:
		line(muted.Render("Remote control makes this machine a Belai host you can drive from the Vulnetix website."))
		line("")
		for _, c := range []struct{ term, desc string }{
			{"host", "`belai rc` runs in the background and shows this machine under Belai → Sessions"},
			{"sessions", "the website starts a Belai session here, then follows and prompts it live"},
			{"where", "only in projects you already trust on this machine (/trusted manages them), and directories you pass with --dir"},
			{"asks", "off: nobody is at this terminal, so the posture and permission rules decide"},
			{"guardrails", "must stay on; remote control will not run without them"},
			{"login", "needs a Vulnetix CLI browser login (vulnetix auth login); API-token logins cannot connect"},
			{"stop", "/rc stop, `belai rc --stop`, or Stop session on the website for one session"},
		} {
			line(fmt.Sprintf("  %s  %s", components.KeyStyle.Render(fmt.Sprintf("%-10s", c.term)), c.desc))
		}
		line("")
		line("Hosts are managed at " + emph.Render(url))
		help = components.HelpBar("enter", "set up", "o", "open the Sessions page", "esc", "back")

	case rcChecklist:
		switch {
		case st.checking && len(st.checks) == 0:
			line(muted.Render("Checking what remote control needs…"))
		default:
			for _, c := range st.checks {
				mark := accent.Render("✓")
				switch c.Level {
				case rc.Warn:
					mark = components.WarnStyle.Render("!")
				case rc.Fail:
					mark = components.DangerStyle.Render("✗")
				}
				line(fmt.Sprintf("%s %s  %s", mark, emph.Render(fmt.Sprintf("%-18s", c.Name)), mcpClean(c.Detail, w-24)))
				if c.Level != rc.OK && c.Fix != "" {
					line(muted.Render(fmt.Sprintf("  %-18s  → %s", "", mcpClean(c.Fix, w-26))))
				}
			}
			if len(st.extraDirs) > 0 {
				line("")
				line(muted.Render("Also offering: " + strings.Join(st.extraDirs, ", ")))
			}
		}
		switch {
		case st.loginBusy && st.grant != nil:
			line("")
			line("Approve this login in your browser:  " + emph.Render(st.grant.VerificationURI))
			line("Check the code matches:  " + components.KeyStyle.Render(st.grant.UserCode))
			line(muted.Render("Waiting for approval…"))
			help = components.HelpBar("o", "open the login page again", "esc", "cancel")
		case st.loginBusy:
			line("")
			line(muted.Render("Starting the Vulnetix login…"))
		case st.loginErr != "":
			line("")
			line(components.DangerStyle.Render("✗ " + mcpClean(st.loginErr, 300)))
		}
		if opts := a.rcOptions(); len(opts) > 0 {
			line("")
			for i, o := range opts {
				label := o.label
				if i == st.sel {
					label = accent.Bold(true).Render(label)
				}
				line(components.Cursor(i == st.sel) + label)
			}
			help = components.HelpBar("↑↓", "choose", "enter", "confirm", "o", "Sessions page", "esc", "back")
		}

	case rcStatus:
		r, live := rc.ReadRecord()
		switch {
		case st.starting:
			line(muted.Render("Starting remote control in the background…"))
		case st.stopping:
			line(muted.Render("Stopping remote control and its sessions…"))
		case live:
			state := accent.Render("●") + " connected"
			if !r.Online {
				state = components.WarnStyle.Render("●") + " not connected yet"
				if r.Error != "" {
					state += muted.Render(" · " + mcpClean(r.Error, 120))
				}
			}
			line(state + muted.Render(fmt.Sprintf(" · pid %d · since %s", r.PID, r.Started.Format("15:04"))))
			line(fmt.Sprintf("%d of %d sessions running", len(r.Sessions), r.Max))
			for _, s := range r.Sessions {
				line(muted.Render(fmt.Sprintf("  %s  %s  started %s", s.ID[:8], s.Cwd, s.Started.Format("15:04"))))
			}
			line("")
			line(fmt.Sprintf("Offering %d director%s:", len(r.Dirs), map[bool]string{true: "y", false: "ies"}[len(r.Dirs) == 1]))
			for i, d := range r.Dirs {
				if i == 8 {
					line(muted.Render(fmt.Sprintf("  … %d more", len(r.Dirs)-8)))
					break
				}
				line(muted.Render("  " + d.Path + "  (" + d.Source + ")"))
			}
			line(muted.Render("Revoke directories in /trusted, then restart remote control."))
			if r.LogPath != "" {
				line("")
				line(muted.Render("log: " + r.LogPath))
			}
		default:
			line("Remote control is not running.")
		}
		if st.note != "" {
			line("")
			style := muted
			if st.noteErr {
				style = components.DangerStyle
			}
			for _, l := range strings.Split(st.note, "\n") {
				line(style.Render(mcpClean(l, w-2)))
			}
		}
		line("")
		line("Manage hosts and start sessions at " + emph.Render(url))
		if live {
			help = components.HelpBar("o/ctrl+y", "open in browser", "s", "stop", "r", "refresh", "esc", "back")
		} else {
			help = components.HelpBar("enter", "set up and start", "o/ctrl+y", "open in browser", "esc", "back")
		}
	}
	if st.note != "" && st.page == rcChecklist {
		line("")
		line(components.DangerStyle.Render(mcpClean(st.note, w-2)))
	}
	if help != "" {
		b.WriteString("\n" + help + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// pollRC refreshes the daemon record the footer shows, at most every few
// seconds unless forced.
func (a *App) pollRC(force bool) {
	if !force && time.Since(a.rcPolled) < 3*time.Second {
		return
	}
	a.rcPolled = time.Now()
	r, live := rc.ReadRecord()
	a.rcLive, a.rcSessions = live, len(r.Sessions)
}

// rcFooterLabel is the footer's rc segment: empty while no daemon runs.
func (a *App) rcFooterLabel() string {
	if !a.rcLive {
		return ""
	}
	return fmt.Sprintf("rc %d", a.rcSessions)
}

// rcCommand implements /rc [start|stop|status|setup].
func (a *App) rcCommand(arg string) tea.Cmd {
	switch strings.TrimSpace(arg) {
	case "", "setup":
		return a.openRC()
	case "start":
		cmd := a.openRC()
		if _, live := rc.ReadRecord(); live {
			return cmd
		}
		return tea.Batch(cmd, a.rcStart())
	case "stop":
		if _, live := rc.ReadRecord(); !live {
			a.addSystem("remote control is not running")
			return nil
		}
		a.addSystem("stopping remote control…")
		return func() tea.Msg {
			_, err := rc.Stop(15 * time.Second)
			if err != nil {
				return copiedMsg{text: "rc stop: " + err.Error()}
			}
			return copiedMsg{text: "remote control stopped · its sessions moved to History"}
		}
	case "status":
		r, live := rc.ReadRecord()
		if !live {
			a.addSystem("remote control is not running · /rc to set it up · hosts: " + rcURL())
			return nil
		}
		a.addSystem(fmt.Sprintf("remote control running (pid %d) · %d of %d sessions · %d directories · hosts: %s",
			r.PID, len(r.Sessions), r.Max, len(r.Dirs), r.URL))
		return nil
	}
	a.addSystem("usage: /rc [start|stop|status|setup]")
	return nil
}
