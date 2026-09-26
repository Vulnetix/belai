package tui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/version"
)

// Session sync (docs/session-sync.md). The host is the source of truth: the
// syncer tails the session JSONL that appendEntry writes, and a prompt typed on
// the website is admitted here, written to that JSONL like a typed prompt, and
// only then reaches the website — through the same tail.

// remotePromptMsg carries one prompt claimed from the website inbox.
type remotePromptMsg sessionsync.RemotePrompt

// syncAuthTTL is how long a resolved Authorization header is reused. Reading
// it can touch the OS keyring, which must not happen on every upload.
const syncAuthTTL = 5 * time.Minute

// startSessionSync starts the syncer when sync is on and a usable Vulnetix
// CLI credential resolves. It records why when it does not. Only a real run
// calls it; New alone (every test) never touches the network.
func (a *App) startSessionSync() {
	if a.syncer != nil {
		return
	}
	a.syncNote = ""
	if !a.settings.SyncEnabled() {
		a.syncNote = "off (sync.enabled is false)"
		return
	}
	workdir := a.workdir
	header, err := credentials.VulnetixAuthHeader(workdir)
	if err != nil {
		a.syncNote = "off (not logged in with the Vulnetix CLI)"
		return
	}
	if err := sessionsync.UsableCredential(header); err != nil {
		a.syncNote = "off (" + err.Error() + ")"
		return
	}
	client, err := sessionsync.NewClient(sessionsync.BaseURL(os.Getenv("VULNETIX_WEB_URL")), cachedAuth(workdir), httpclient.Default())
	if err != nil {
		a.syncNote = "off (" + err.Error() + ")"
		return
	}
	dir, err := config.GlobalDir()
	if err != nil {
		a.syncNote = "off (" + err.Error() + ")"
		return
	}
	hostID, err := sessionsync.HostID(dir)
	if err != nil {
		a.syncNote = "off (" + err.Error() + ")"
		return
	}
	a.syncer = sessionsync.New(sessionsync.Options{
		Client:        client,
		HostID:        hostID,
		Host:          sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version},
		RemotePrompts: a.settings.SyncRemotePromptsEnabled(),
	})
	a.syncer.Start(context.Background())
	a.syncedID = ""
	// A resumed session is live on this host now, even before its next line.
	a.syncTouch()
}

// retrySessionSync starts sync after a login inside the TUI, when startup had
// found no credential. A run that never tried (every test) stays offline.
func (a *App) retrySessionSync() tea.Cmd {
	if a.syncer != nil || a.syncNote == "" || !a.settings.SyncEnabled() {
		return nil
	}
	a.startSessionSync()
	return a.watchRemotePrompts()
}

// cachedAuth reads the CLI credential at most once per syncAuthTTL.
func cachedAuth(workdir string) func() (string, error) {
	var mu sync.Mutex
	var header string
	var at time.Time
	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if header != "" && time.Since(at) < syncAuthTTL {
			return header, nil
		}
		h, err := credentials.VulnetixAuthHeader(workdir)
		if err != nil {
			return "", err
		}
		if err := sessionsync.UsableCredential(h); err != nil {
			return "", err
		}
		header, at = h, time.Now()
		return header, nil
	}
}

// syncTouch points the syncer at the current session (once its file exists)
// and asks it to read the lines just written. appendEntry calls it after
// every successful write, so the website mirrors exactly what is on disk.
func (a *App) syncTouch() {
	if a.syncer == nil || a.store == nil || a.storeDisabled || a.sessionID == "" {
		return
	}
	if a.syncedID != a.sessionID {
		path := a.store.SessionPath(a.sessionKey, a.sessionID)
		if _, err := os.Stat(path); err != nil {
			return
		}
		a.syncer.Activate(sessionsync.SessionInfo{
			ID:              a.sessionID,
			Path:            path,
			ProjectKey:      string(a.sessionKey),
			ProjectName:     a.sessionKey.Project(),
			Cwd:             a.workdir,
			Name:            a.sessionName,
			Model:           a.cfg.Model,
			Provider:        a.cfg.Provider,
			Mode:            a.mode,
			ParentSessionID: a.parentSession,
		})
		a.syncedID = a.sessionID
	}
	a.syncer.UpdateMeta(a.sessionID, a.cfg.Model, a.cfg.Provider, a.mode)
	a.syncer.Nudge()
}

// closeSync flushes the mirror and moves the live session to History.
func (a *App) closeSync() {
	if a.syncer == nil {
		return
	}
	a.syncer.Close(3 * time.Second)
	a.syncer = nil
	a.syncedID = ""
	a.remoteQueue = nil
}

// watchRemotePrompts waits for the next website prompt.
func (a *App) watchRemotePrompts() tea.Cmd {
	if a.syncer == nil || !a.syncer.RemotePromptsEnabled() {
		return nil
	}
	ch := a.syncer.Prompts()
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return nil
		}
		return remotePromptMsg(p)
	}
}

// remoteBusy reports whether a website prompt must wait: a turn is running or
// being prepared, or the user is on a screen other than the transcript.
func (a *App) remoteBusy() bool {
	return a.working() || a.preSend || a.view != viewChat
}

// handleRemotePrompt admits one website prompt the same way a typed prompt is
// admitted, or queues it behind the running turn.
func (a *App) handleRemotePrompt(p sessionsync.RemotePrompt) tea.Cmd {
	if a.syncer == nil {
		return nil
	}
	if p.SessionID != a.sessionID {
		a.syncer.Ack(p.ID, sessionsync.AckRefused, "the session is no longer active on the host", "")
		return nil
	}
	if a.remoteBusy() {
		a.remoteQueue = append(a.remoteQueue, p)
		a.syncer.Ack(p.ID, sessionsync.AckQueued, "", "")
		return nil
	}
	return a.submitRemote(p)
}

// drainRemoteQueue submits the oldest queued website prompt once the host is
// idle. It runs on every tick.
func (a *App) drainRemoteQueue() tea.Cmd {
	if len(a.remoteQueue) == 0 || a.syncer == nil || a.remoteBusy() {
		return nil
	}
	p := a.remoteQueue[0]
	a.remoteQueue = a.remoteQueue[1:]
	if p.SessionID != a.sessionID {
		a.syncer.Ack(p.ID, sessionsync.AckRefused, "the session is no longer active on the host", "")
		return nil
	}
	return a.submitRemote(p)
}

// submitRemote runs a website prompt as a typed prompt would run, minus the
// composer: the user's draft and pending attachments are left alone, and a
// leading "/" or "!" is plain text — the website can never run a slash
// command or a shell command. Admission (sanitize + classifier) happens in
// the turn exactly as for a typed prompt, and a refusal lands in the
// transcript, which the website then shows.
func (a *App) submitRemote(p sessionsync.RemotePrompt) tea.Cmd {
	text := sessionsync.CleanPrompt(p.Content)
	if text == "" {
		a.syncer.Ack(p.ID, sessionsync.AckRefused, "the prompt was empty after cleaning", "")
		return nil
	}
	// Agent mode with no carrier waits on the host's agent picker; the
	// website cannot answer it.
	if a.mode == "agent" && a.namedAgent == "" {
		a.syncer.Ack(p.ID, sessionsync.AckRefused, "the host is in agent mode with no agent selected; choose one on the host", "")
		return nil
	}
	firstUser := !a.hasUserMessage()
	before := a.lastEntryID
	a.echoUserMessage(components.Message{Role: "user", Content: text, RemoteID: p.ID})
	entryID := ""
	if a.lastEntryID != before {
		entryID = a.lastEntryID
	}
	a.syncer.Ack(p.ID, sessionsync.AckAccepted, "", entryID)
	return a.dispatchPrompt(text, nil, "", firstUser)
}

// userEntry is the session entry for a user message; a website prompt keeps
// its origin and request id.
func userEntry(m components.Message) session.Entry {
	e := session.Entry{Type: "user", Role: "user", Content: m.Text()}
	if m.RemoteID != "" {
		e.Meta = map[string]any{"source": "web", "remote_prompt_id": m.RemoteID}
	}
	return e
}

// syncCommand implements /sync [status|on|off|backfill].
func (a *App) syncCommand(arg string) tea.Cmd {
	switch strings.TrimSpace(arg) {
	case "", "status":
		a.addSystem(a.syncStatusText())
		return nil
	case "on", "off":
		on := strings.TrimSpace(arg) == "on"
		if err := config.Mutate(config.ScopeGlobal, a.workdir, func(s *config.Settings) error {
			if s.Sync == nil {
				s.Sync = &config.SyncSettings{}
			}
			s.Sync.Enabled = &on
			return nil
		}); err != nil {
			a.addSystem("sync: " + err.Error())
			return nil
		}
		if err := a.reloadSettings(); err != nil {
			a.addSystem("sync: " + err.Error())
		}
		if !on {
			a.closeSync()
			a.syncNote = "off (sync.enabled is false)"
			a.addSystem("session sync off: this and future sessions stay on this machine")
			return nil
		}
		a.startSessionSync()
		a.addSystem(a.syncStatusText())
		return a.watchRemotePrompts()
	case "backfill":
		return a.syncBackfill()
	default:
		a.addSystem("usage: /sync [status|on|off|backfill]")
		return nil
	}
}

func (a *App) syncStatusText() string {
	if a.syncer == nil {
		note := a.syncNote
		if note == "" {
			note = "off"
		}
		return "session sync: " + note
	}
	st := a.syncer.Status()
	var b strings.Builder
	b.WriteString("session sync: on")
	if a.syncer.RemotePromptsEnabled() {
		b.WriteString(" · web prompts on")
	} else {
		b.WriteString(" · view-only (sync.remote_prompts is false)")
	}
	switch {
	case st.Registered:
		fmt.Fprintf(&b, "\n  this session: %d lines on the website", st.LastSeq+1)
	default:
		b.WriteString("\n  this session: appears on the website after its first line")
	}
	if len(a.remoteQueue) > 0 {
		fmt.Fprintf(&b, "\n  %d web prompt(s) queued behind the running turn", len(a.remoteQueue))
	}
	if st.LastError != "" {
		b.WriteString("\n  last error: " + st.LastError)
	}
	b.WriteString("\n  History and Sessions: https://www.vulnetix.com/resolve/belai-history")
	return b.String()
}

// syncBackfillDoneMsg reports a /sync backfill.
type syncBackfillDoneMsg struct {
	n   int
	err error
}

// syncBackfill uploads this project's earlier sessions to History.
func (a *App) syncBackfill() tea.Cmd {
	if a.syncer == nil {
		a.addSystem(a.syncStatusText())
		return nil
	}
	if a.store == nil {
		return nil
	}
	infos, err := a.store.SessionsIn(a.sessionKey)
	if err != nil {
		a.addSystem("sync backfill: " + err.Error())
		return nil
	}
	var todo []sessionsync.SessionInfo
	for _, s := range infos {
		if s.ID == a.sessionID {
			continue
		}
		todo = append(todo, sessionsync.SessionInfo{
			ID: s.ID, Path: a.store.SessionPath(a.sessionKey, s.ID),
			ProjectKey: string(a.sessionKey), ProjectName: a.sessionKey.Project(),
			Model: s.Model, Provider: s.Provider,
		})
	}
	if len(todo) == 0 {
		a.addSystem("sync backfill: no earlier sessions in this project")
		return nil
	}
	a.addSystem(fmt.Sprintf("sync backfill: uploading %d earlier session(s) to History…", len(todo)))
	s := a.syncer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n, err := s.Backfill(ctx, todo)
		return syncBackfillDoneMsg{n: n, err: err}
	}
}
