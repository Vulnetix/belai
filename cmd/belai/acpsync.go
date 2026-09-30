package main

import (
	"context"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/version"
)

// acpMirror mirrors the editor sessions of one `belai acp` connection to the
// Vulnetix website through the ordinary session syncer. It uploads only lines
// the session log already wrote. It takes no remote prompts and no remote
// answers: the editor owns the conversation, so nothing from the website can
// drive the session or answer its asks. The syncer follows one live session
// at a time; a touch on another session switches to it.
type acpMirror struct {
	syncer *sessionsync.Syncer
	store  *session.Store
	model  string
	prov   string

	mu    sync.Mutex
	infos map[string]sessionsync.SessionInfo
	live  string
}

// newACPMirror starts the mirror, or returns nil when session sync is off, no
// usable Vulnetix CLI credential resolves or the state directory is unusable.
// The check is silent: an editor session works the same without it.
func newACPMirror(settings config.Settings, workdir, provider, model string) *acpMirror {
	if !settings.SyncEnabled() {
		return nil
	}
	header, err := credentials.VulnetixAuthHeader(workdir)
	if err != nil || sessionsync.UsableCredential(header) != nil {
		return nil
	}
	client, err := sessionsync.NewClient(sessionsync.BaseURL(os.Getenv("VULNETIX_WEB_URL")),
		func() (string, error) { return header, nil }, httpclient.Default())
	if err != nil {
		return nil
	}
	store, err := session.NewStore()
	if err != nil {
		return nil
	}
	syncer := sessionsync.New(sessionsync.Options{
		Client:        client,
		HostID:        headless.HostID(),
		Host:          sessionsync.Host{Hostname: sessionsync.Hostname(), OS: runtime.GOOS, BelaiVersion: version.Version},
		RemotePrompts: false,
		RemoteAnswers: false,
	})
	syncer.Start(context.Background())
	return &acpMirror{syncer: syncer, store: store, model: model, prov: provider, infos: map[string]sessionsync.SessionInfo{}}
}

// Opened implements acp.Mirror.
func (m *acpMirror) Opened(id, cwd, name string) {
	key, err := session.KeyFor(cwd)
	if err != nil {
		return
	}
	info := sessionsync.SessionInfo{
		ID: id, Path: m.store.SessionPath(key, id), ProjectKey: string(key), ProjectName: key.Project(),
		Cwd: cwd, Name: name, Model: m.model, Provider: m.prov, Mode: "agent",
	}
	m.mu.Lock()
	m.infos[id] = info
	m.live = id
	m.mu.Unlock()
	m.syncer.Activate(info)
}

// Touched implements acp.Mirror.
func (m *acpMirror) Touched(id string) {
	m.mu.Lock()
	info, ok := m.infos[id]
	moved := ok && m.live != id
	if moved {
		m.live = id
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if moved {
		m.syncer.Activate(info)
	}
	m.syncer.Nudge()
}

// Close implements acp.Mirror: it uploads what is left and ends the live
// session on the website.
func (m *acpMirror) Close() { m.syncer.Close(3 * time.Second) }
