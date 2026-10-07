package rc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/avatar"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/teleport/changes"
)

// DefaultMax is how many sessions a daemon runs at once unless told otherwise.
const DefaultMax = 3

// DefaultIdle is how long a started session waits for a follow-up prompt
// before it ends on its own.
const DefaultIdle = 30 * time.Minute

// Options configure the daemon.
type Options struct {
	// Exe is the belai binary each session runs as `belai rc-session`.
	Exe    string
	Client *sessionsync.Client
	HostID string
	Host   sessionsync.Host
	Dirs   []Dir
	Max    int
	// MaxWorkers, when set, is the fleet worker cap for workers this daemon
	// starts and reports, in place of agents.max_workers (belai rc --max).
	MaxWorkers int
	Idle       time.Duration
	URL        string
	// Out receives one line per event (stderr, or the log when detached).
	Out     io.Writer
	LogPath string

	HeartbeatEvery time.Duration // 15s
	PollWait       time.Duration // 25s
	// Start replaces the child spawn in tests.
	Start func(c Child) (pid int, wait func() error, err error)
	// Inventory reads the host's worker profiles, crews and live workers
	// (LocalInventory unless a test replaces it).
	Inventory func() Inventory
	// Knowledge reads the knowledge catalogue for the offered directories
	// (localKnowledge unless a test replaces it).
	Knowledge func([]Dir) []sessionsync.RCKnowledge
	// Models says which providers this host can run a web session on
	// (LocalModels unless a test replaces it); a start request that names a
	// provider outside it is refused.
	Models func() *sessionsync.RCModels
	// StartWorkers runs `belai agent start` for a worker or crew request
	// (runAgentStart unless a test replaces it). It returns the command's
	// report, or an error whose text is the refusal reason.
	StartWorkers func(w WorkerStart) (string, error)
	// PauseWorker asks a live worker of this host to pause or resume
	// (setWorkerPaused unless a test replaces it). The error text is the
	// refusal reason.
	PauseWorker func(id string, pause bool) error

	// Schedules is the host's stored schedules (internal/schedule). nil means
	// this daemon fires none and syncs none.
	Schedules *schedule.Store
	// ScheduleRemote is the website half of schedule sync (the sync client
	// unless a test replaces it).
	ScheduleRemote schedule.Remote
	// ScheduleEvery is how often stored schedules are checked and synced
	// (30s).
	ScheduleEvery time.Duration
	// Now is the scheduler's clock (time.Now unless a test replaces it).
	Now func() time.Time
	// Busy reports whether a worker of the profile is already live in the
	// directory's repository (the fleet registry unless a test replaces it).
	Busy func(profile, dir string) bool
	// RemotePrompts reports whether the website may send this host text for a
	// model to read: the user's own sync.remote_prompts (an avatar request needs
	// it; a profile install does not, being the user's own action). It reads the
	// global settings and fails closed unless a test replaces it.
	RemotePrompts func() bool
	// TeleportPush is this host's push policy for a teleport (config.TeleportPush*:
	// the user's teleport.push, read from the global settings each time); nil is ask.
	TeleportPush func() string
	// TeleportGit builds the git runners a teleport_code request reads and pushes
	// with; nil is the hardened default (changes.Hardened). Tests replace it.
	TeleportGit changes.Runners
	// Distill writes the hand-over of a replay's changes with this host's model
	// (the teleport_distill role). nil, or a model that gives nothing usable, is the
	// harness's file list.
	Distill func(ctx context.Context, files []rolemanager.TeleportFile, skipped []string, patch string) rolemanager.Distilled
	// DrawAvatar draws a customised Pix with this host's main model for an
	// "avatar" request. It returns the SVG, or the reason it could not (harness
	// text). nil means this daemon has no model to draw with and refuses.
	DrawAvatar func(ctx context.Context, r avatar.Request) ([]byte, string)
	// Index indexes the documents of a freshly installed profile: it runs
	// `belai agent knowledge -index NAME` from a trusted directory this daemon
	// offers (runKnowledgeIndex unless a test replaces it). It returns a short
	// clause for the acknowledgement, or the reason it could not.
	Index func(ctx context.Context, exe, dir, profile string) (string, error)
	// SyncProfiles reports whether the host keeps the website's agent and crew
	// library current by itself: the user's own sync.profiles, read from the
	// global settings each check and failing closed unless a test replaces it.
	SyncProfiles func() bool
	// SyncItem reports whether the host keeps the website's library of one kind of
	// item (skill, prompt, ...) current by itself and takes backup and install
	// requests for it: the user's own sync.<kinds>, read from the global settings
	// each time and failing closed unless a test replaces it.
	SyncItem func(kind libitem.Kind) bool
	// LibraryRemote is the server half of that sync (the sync client unless a
	// test replaces it).
	LibraryRemote LibraryRemote
	// LibrarySyncEvery is how often profiles, crews and items are checked (30s).
	LibrarySyncEvery time.Duration

	// Controls lets web sessions change their own controls (--web-controls,
	// internal/sessionctl); GuardrailsOff also lets them turn guardrails off
	// (--web-allow-guardrails-off). Both reach a session as fixed argv.
	Controls      bool
	GuardrailsOff bool
	// ProjectSettings advertises each offered directory's project preferences
	// and takes "project_prefs" requests (--web-project-settings).
	ProjectSettings bool
	// Shell lets web sessions run a shell line on this host (--web-shell); it
	// reaches a session as fixed argv.
	Shell bool
	// Prefs reads the offered directories' preferences (localPrefs unless a
	// test replaces it).
	Prefs func([]Dir) map[string]DirPrefs
}

// WorkerStart is one validated worker or crew start.
type WorkerStart struct {
	Exe, Cwd string
	// Profile or Crew; exactly one is set.
	Profile, Crew string
	// Provider and Model are the request's model for these workers; empty
	// means the host's own default (belai agent start -provider -model).
	Provider, Model string
	// MaxWorkers overrides agents.max_workers for this start when set.
	MaxWorkers int
	// Drain makes the workers exit once nothing is left to claim, whatever
	// cron schedule their profile carries (a stored schedule fires them).
	Drain bool
	// Fill starts only the replicas a crew lacks in the directory.
	Fill bool
	// Controls and GuardrailsOff are the daemon's own --web-controls and
	// --web-allow-guardrails-off, passed on so the workers take session controls.
	Controls, GuardrailsOff bool
}

// Child is one session to start.
type Child struct {
	Exe, Cwd, Dispatch, SessionID, Mode, Prompt, LogPath string
	// Dirs are the extra workspace directories (/add-dir), each already
	// checked against the directories the host offers.
	Dirs []string
	// Provider, Model and Effort are the request's override; empty means the
	// host's own default (or its routing table).
	Provider, Model, Effort string
	// Profile is the agent profile a web session is engaged with (agent mode
	// only); empty runs the default agent.
	Profile string
	// GitSync is the request's switch for the git sync before turns; nil
	// leaves it to git.sync in the host's settings.
	GitSync *bool
	Idle    time.Duration
	// Controls and GuardrailsOff are the daemon's own flags, passed on.
	Controls, GuardrailsOff bool
	// Shell is the daemon's --web-shell, passed on.
	Shell bool
}

// Daemon is a running `belai rc`.
type Daemon struct {
	o Options

	mu       sync.Mutex
	sessions map[string]*child
	online   bool
	lastErr  string
	catalog  string            // Inventory.catalogueHash of the last advertisement
	schedErr map[string]string // per sync step, the last failure logged ("" when it works)
	started  time.Time
	wg       sync.WaitGroup
	// avatarSlot holds one token while an avatar is being drawn.
	avatarSlot chan struct{}
	// codeSlot holds a token for each teleport_code request being answered.
	codeSlot chan struct{}
	// libsync is the automatic sync's memory of what it has settled (autosync.go).
	libsync *libSyncState
	// scanSlot holds a token while a library scan runs; importMu serialises the
	// library imports a website selection fans out (scan.go).
	scanSlot chan struct{}
	importMu sync.Mutex
}

type child struct {
	RunningSession
}

// New validates options and builds a daemon.
func New(o Options) (*Daemon, error) {
	if o.Client == nil || o.HostID == "" {
		return nil, errors.New("rc: a sync client and host id are required")
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	if o.Idle <= 0 {
		o.Idle = DefaultIdle
	}
	if o.HeartbeatEvery <= 0 {
		o.HeartbeatEvery = 15 * time.Second
	}
	if o.PollWait <= 0 {
		o.PollWait = 25 * time.Second
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Start == nil {
		o.Start = startChild
	}
	if o.Inventory == nil {
		o.Inventory = LocalInventory
	}
	if o.Models == nil {
		o.Models = LocalModels
	}
	if o.Knowledge == nil {
		o.Knowledge = localKnowledge
	}
	{
		// The knowledge catalogue covers the offered directories, which only
		// the daemon knows.
		read, dirs, cat := o.Inventory, o.Dirs, o.Knowledge
		o.Inventory = func() Inventory {
			inv := read()
			inv.Knowledge = cat(dirs)
			return inv
		}
	}
	if o.ProjectSettings {
		if o.Prefs == nil {
			o.Prefs = localPrefs
		}
		read, dirs, prefs := o.Inventory, o.Dirs, o.Prefs
		o.Inventory = func() Inventory {
			inv := read()
			inv.Prefs = prefs(dirs)
			return inv
		}
	}
	if o.MaxWorkers > 0 {
		read := o.Inventory
		o.Inventory = func() Inventory {
			inv := read()
			inv.MaxWorkers = o.MaxWorkers
			return inv
		}
	}
	if o.PauseWorker == nil {
		o.PauseWorker = setWorkerPaused
	}
	if o.StartWorkers == nil {
		o.StartWorkers = runAgentStart
	}
	if o.ScheduleEvery <= 0 {
		o.ScheduleEvery = DefaultScheduleEvery
	}
	if o.ScheduleRemote == nil {
		o.ScheduleRemote = o.Client
	}
	if o.Busy == nil {
		o.Busy = localBusy
	}
	if o.RemotePrompts == nil {
		o.RemotePrompts = remotePromptsOn
	}
	if o.Index == nil {
		o.Index = runKnowledgeIndex
	}
	if o.SyncProfiles == nil {
		o.SyncProfiles = syncProfilesOn
	}
	if o.SyncItem == nil {
		o.SyncItem = syncItemOn
	}
	if o.LibraryRemote == nil {
		o.LibraryRemote = o.Client
	}
	if o.LibrarySyncEvery <= 0 {
		o.LibrarySyncEvery = DefaultLibrarySyncEvery
	}
	return &Daemon{o: o, sessions: map[string]*child{}, started: time.Now(), avatarSlot: make(chan struct{}, 1),
		codeSlot: make(chan struct{}, 2), scanSlot: make(chan struct{}, 1), libsync: &libSyncState{records: map[string]syncRecord{}}}, nil
}

func (d *Daemon) logf(format string, args ...any) {
	fmt.Fprintf(d.o.Out, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Run serves until ctx ends, then stops every session it started and tells
// the website the host is offline.
func (d *Daemon) Run(ctx context.Context) error {
	if r, live := ReadRecord(); live && r.PID != os.Getpid() {
		return fmt.Errorf("belai rc is already running (pid %d); stop it with `belai rc --stop`", r.PID)
	}
	d.writeRecord()
	defer removeRecord(os.Getpid())

	// The daemon's own audit stream: the host's remote-control sessions and
	// the requests it served (docs/audit.md). Best effort.
	if dir, err := config.GlobalDir(); err == nil {
		defer sessionsync.StartAudit(context.Background(), d.o.Client, d.o.HostID, d.o.Host, dir).Close(5 * time.Second)
	}

	d.register(ctx, d.o.Inventory())
	audit.Emit(audit.Fact{Kind: audit.HostRCOnline, ActorKind: audit.ActorHarness,
		Data: map[string]string{"version": d.o.Host.BelaiVersion}})
	d.logf("remote control on · %d director%s offered · up to %d sessions", len(d.o.Dirs), plural(len(d.o.Dirs), "y", "ies"), d.o.Max)
	if d.o.MaxWorkers > 0 {
		d.logf("up to %d fleet workers (overrides agents.max_workers)", d.o.MaxWorkers)
	}
	d.logf("start sessions at %s", d.o.URL)

	go d.heartbeat(ctx)
	if d.o.Schedules != nil {
		d.wg.Add(1)
		go d.scheduler(ctx)
	}
	d.wg.Add(1)
	go d.librarySync(ctx)
	d.poll(ctx)

	d.shutdown()
	return nil
}

// register advertises the host and its worker catalogue, retrying until it
// succeeds or ctx ends.
func (d *Daemon) register(ctx context.Context, inv Inventory) {
	h := d.o.Host
	h.RC = &sessionsync.RCInfo{MaxSessions: d.o.Max, MaxWorkers: inv.MaxWorkers,
		Profiles: inv.Profiles, Crews: inv.Crews, Agents: inv.Agents, Items: inv.Items, Commands: inv.Commands, Models: inv.Models, Knowledge: inv.Knowledge,
		Controls: d.o.Controls, GuardrailsOff: d.o.Controls && d.o.GuardrailsOff, ProjectSettings: d.o.ProjectSettings}
	if d.o.Controls {
		for _, c := range sessionctl.Controls {
			h.RC.ControlCatalogue = append(h.RC.ControlCatalogue, sessionsync.RCControl{
				ID: c.ID, Command: c.Command, Usage: c.Usage, Keys: c.Keys, Values: c.Values, Kind: c.Kind,
			})
		}
	}
	if h.RC.Commands == nil {
		h.RC.Commands = []sessionsync.RCCommand{}
	}
	if h.RC.Items == nil {
		h.RC.Items = []sessionsync.RCItem{}
	}
	if h.RC.Profiles == nil {
		h.RC.Profiles = []sessionsync.RCProfile{}
	}
	if h.RC.Crews == nil {
		h.RC.Crews = []sessionsync.RCCrew{}
	}
	d.mu.Lock()
	d.catalog = inv.catalogueHash()
	d.mu.Unlock()
	for _, dir := range d.o.Dirs {
		rd := dirGit(sessionsync.RCDir{Path: dir.Path, Name: dir.Name, Source: dir.Source})
		if p, ok := inv.Prefs[dir.Path]; ok {
			rd.Prefs, rd.Effective = p.Prefs, p.Effective
		}
		h.RC.Dirs = append(h.RC.Dirs, rd)
	}
	backoff := time.Second
	for {
		err := d.o.Client.PutHost(ctx, d.o.HostID, h)
		d.setOnline(err)
		if err == nil || ctx.Err() != nil {
			return
		}
		d.logf("register: %v (retrying in %s)", err, backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, time.Minute)
	}
}

func (d *Daemon) heartbeat(ctx context.Context) {
	t := time.NewTicker(d.o.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		inv := d.o.Inventory()
		d.mu.Lock()
		changed := inv.catalogueHash() != d.catalog
		d.mu.Unlock()
		if changed {
			// A profile or crew was added, edited or removed: advertise again.
			d.register(ctx, inv)
		}
		err := d.o.Client.RCHeartbeat(ctx, d.o.HostID, d.running(), inv.Workers)
		if errors.Is(err, sessionsync.ErrNotFound) {
			// The server lost the host (or never had it): advertise again.
			d.register(ctx, inv)
			err = nil
		}
		if err != nil && ctx.Err() == nil {
			d.logf("heartbeat: %v", err)
		}
		d.setOnline(err)
		d.writeRecord()
	}
}

func (d *Daemon) poll(ctx context.Context) {
	var backoff time.Duration
	for ctx.Err() == nil {
		reqs, err := d.o.Client.Dispatches(ctx, d.o.HostID, d.o.PollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(max(2*backoff, 2*time.Second), time.Minute)
			d.logf("poll: %v (retrying in %s)", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		for _, r := range reqs {
			d.handle(ctx, r)
		}
	}
}

// handle applies one request. Every field is untrusted.
func (d *Daemon) handle(ctx context.Context, r sessionsync.Dispatch) {
	// Every request is acknowledged through here, so the audit log holds each
	// one with its kind and outcome. The kind is reduced to a known word: the
	// request is the website's, and only the word the host recognised is kept.
	ack := func(ctx context.Context, id, status, sid, reason string) {
		kind := "unknown"
		switch r.Kind {
		case "start", "stop", "worker", "crew", "pause", "resume", "profile_backup", "profile_install", "crew_backup", "crew_install", "avatar",
			"item_backup", "item_install", "provider_keys_install", "provider_keys_remove", "mcp_secrets_install", "mcp_secrets_remove", "library_sync", "library_scan", "library_import",
			"project_prefs", "teleport_backup", "teleport_code":
			kind = r.Kind
		}
		audit.Emit(audit.Fact{Kind: audit.HostDispatch, ActorKind: audit.ActorWeb,
			Data: map[string]string{"dispatch": kind, "status": status}})
		d.ack(ctx, id, status, sid, reason)
	}
	switch r.Kind {
	case "start":
		sid, reason := d.start(r)
		if reason != "" {
			d.logf("refused session in %s: %s", r.Cwd, reason)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", reason)
			return
		}
		ack(ctx, r.ID, sessionsync.DispatchStarted, sid, "")
	case "stop":
		if d.stop(r.SessionID) {
			d.logf("stopping session %s", short(r.SessionID))
			ack(ctx, r.ID, sessionsync.DispatchStopped, r.SessionID, "")
			return
		}
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "that session is not running under belai rc on this host")
	case "worker", "crew":
		report, reason := d.startWorkers(r)
		if reason != "" {
			d.logf("refused %s %s%s in %s: %s", r.Kind, r.Profile, r.Crew, r.Cwd, reason)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", reason)
			return
		}
		d.logf("started %s %s%s in %s", r.Kind, r.Profile, r.Crew, r.Cwd)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "pause", "resume":
		if !fleet.ValidID(r.Worker) {
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", "that is not a worker id")
			return
		}
		if err := d.o.PauseWorker(r.Worker, r.Kind == "pause"); err != nil {
			d.logf("refused %s %s: %v", r.Kind, r.Worker, err)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", clip(err.Error()))
			return
		}
		d.logf("%s %s", r.Kind, r.Worker)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", r.Worker+" "+r.Kind+"d")
	case "avatar":
		// Answered in the background, so a slow model never holds the queue.
		d.startAvatar(ctx, r, ack)
	case "profile_backup", "profile_install":
		var report, why string
		if r.Kind == "profile_backup" {
			report, why = d.backupProfile(ctx, r)
		} else {
			report, why = d.installProfile(ctx, r)
		}
		if why != "" {
			d.logf("refused %s %s%s: %s", r.Kind, short(r.Library), sanitizeName(r.Profile), why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("%s: %s", r.Kind, report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "crew_backup", "crew_install":
		var report, why string
		if r.Kind == "crew_backup" {
			report, why = d.backupCrew(ctx, r)
		} else {
			report, why = d.installCrew(ctx, r)
		}
		if why != "" {
			d.logf("refused %s %s%s: %s", r.Kind, short(r.Library), sanitizeName(r.Crew), why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("%s: %s", r.Kind, report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "teleport_backup":
		// Backs up the session's profile, the crews that list it and their
		// members, so the host that continues the session can install them.
		report, why := d.backupForTeleport(ctx, r)
		if why != "" {
			d.logf("refused teleport_backup %s: %s", sanitizeName(strings.Join(r.Profiles, ",")), why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("teleport_backup: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "teleport_code":
		// Answered in the background, so a model's hand-over never holds the queue.
		d.startTeleportCode(ctx, r, ack)
	case "item_backup", "item_install":
		var report, why string
		if r.Kind == "item_backup" {
			report, why = d.backupItem(ctx, r)
		} else {
			report, why = d.installItem(ctx, r)
		}
		if why != "" {
			d.logf("refused %s %s %s: %s", r.Kind, sanitizeName(r.ItemKind), sanitizeName(r.Name), why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("%s: %s", r.Kind, report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "library_scan":
		// Answered in the background: a scan walks directories for up to 25 seconds.
		d.startLibraryScan(ctx, r, ack)
	case "library_import":
		// Answered in the background, one import at a time.
		d.startLibraryImport(ctx, r, ack)
	case "library_sync":
		// Counts go in the acknowledgement; no item name or document does.
		report, why := d.librarySyncNow(ctx)
		if why != "" {
			d.logf("refused library_sync: %s", why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("library_sync: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "provider_keys_install":
		// Slugs go in the log and the acknowledgement; a key never does.
		report, why := d.installProviderKeys(ctx, r)
		if why != "" {
			d.logf("refused provider_keys_install: %s", why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("provider_keys_install: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "provider_keys_remove":
		// Slugs only, as for an install; nothing is fetched.
		report, why := d.removeProviderKeys(r)
		if why != "" {
			d.logf("refused provider_keys_remove: %s", why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("provider_keys_remove: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "mcp_secrets_install":
		// Key names go in the log and the acknowledgement; a secret never does.
		report, why := d.installMCPSecrets(ctx, r)
		if why != "" {
			d.logf("refused mcp_secrets_install: %s", why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("mcp_secrets_install: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "mcp_secrets_remove":
		// Names only; nothing is fetched.
		report, why := d.removeMCPSecrets(r)
		if why != "" {
			d.logf("refused mcp_secrets_remove: %s", why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("mcp_secrets_remove: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
	case "project_prefs":
		// Key names and counts go in the log and the acknowledgement; values
		// are fixed words, booleans and numbers checked by the host.
		report, why := d.setProjectPrefs(r)
		if why != "" {
			d.logf("refused project_prefs in %s: %s", r.Cwd, why)
			ack(ctx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("project_prefs: %s", report)
		ack(ctx, r.ID, sessionsync.DispatchStarted, "", report)
		// Advertise the new values now rather than at the next heartbeat.
		d.register(ctx, d.o.Inventory())
	default:
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "this Belai does not understand that request; update Belai on the host")
	}
}

// sanitizeName reduces a profile name from a request to a short identifier for
// the log.
func sanitizeName(s string) string { return sanitize.Ident(s, 64) }

func (d *Daemon) ack(ctx context.Context, id, status, sid, reason string) {
	if err := d.o.Client.AckDispatch(ctx, id, status, sid, reason); err != nil && ctx.Err() == nil {
		d.logf("ack %s: %v", short(id), err)
	}
}

// start validates a start request and spawns its session. It returns the
// new session id, or the reason it refused.
func (d *Daemon) start(r sessionsync.Dispatch) (string, string) {
	cwd, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "this host does not offer that directory"
	}
	switch r.Mode {
	case "", "agent", "plan", "goal", "code":
	default:
		return "", "unknown mode " + fmt.Sprintf("%q", r.Mode)
	}
	dirs, ok := AllowedExtra(d.o.Dirs, cwd, r.Dirs)
	if !ok {
		return "", "this host does not offer one of those directories"
	}
	prompt := sessionsync.CleanPrompt(r.Prompt)
	if prompt == "" {
		return "", "the prompt was empty after cleaning"
	}
	if why := checkOverride(r.Provider, r.Model, r.Effort, d.o.Models); why != "" {
		return "", why
	}
	if why := d.checkAgentProfile(r.Profile, r.Mode); why != "" {
		return "", why
	}
	d.mu.Lock()
	if len(d.sessions) >= d.o.Max {
		n := len(d.sessions)
		d.mu.Unlock()
		return "", fmt.Sprintf("this host is already running %d of %d sessions", n, d.o.Max)
	}
	d.mu.Unlock()

	sid, err := session.NewID()
	if err != nil {
		return "", "could not mint a session id"
	}
	c := Child{
		Exe: d.o.Exe, Cwd: cwd, Dirs: dirs, Dispatch: r.ID, SessionID: sid, Mode: r.Mode, Prompt: prompt,
		Provider: r.Provider, Model: r.Model, Effort: r.Effort, Profile: r.Profile, GitSync: r.GitSync,
		Idle: d.o.Idle, LogPath: d.sessionLog(sid),
		Controls: d.o.Controls, GuardrailsOff: d.o.Controls && d.o.GuardrailsOff,
		Shell: d.o.Shell,
	}
	pid, wait, err := d.o.Start(c)
	if err != nil {
		return "", "could not start Belai: " + err.Error()
	}
	d.mu.Lock()
	d.sessions[sid] = &child{RunningSession{ID: sid, Dispatch: r.ID, Cwd: cwd, PID: pid, Started: time.Now()}}
	d.mu.Unlock()
	d.writeRecord()
	d.logf("started session %s in %s (pid %d)", short(sid), cwd, pid)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		err := wait()
		d.mu.Lock()
		delete(d.sessions, sid)
		d.mu.Unlock()
		d.writeRecord()
		if err != nil {
			d.logf("session %s ended: %v (log: %s)", short(sid), err, c.LogPath)
		} else {
			d.logf("session %s ended", short(sid))
		}
	}()
	return sid, ""
}

func (d *Daemon) stop(sid string) bool {
	d.mu.Lock()
	c, ok := d.sessions[strings.ToLower(sid)]
	d.mu.Unlock()
	if !ok {
		return false
	}
	_ = proc.Terminate(c.PID)
	return true
}

// shutdown stops every session and marks the host offline.
func (d *Daemon) shutdown() {
	d.mu.Lock()
	var pids []int
	for _, c := range d.sessions {
		pids = append(pids, c.PID)
	}
	d.mu.Unlock()
	if len(pids) > 0 {
		d.logf("stopping %d session%s", len(pids), plural(len(pids), "", "s"))
	}
	for _, pid := range pids {
		_ = proc.Terminate(pid)
	}
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		for _, pid := range pids {
			_ = proc.Kill(pid)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.o.Client.RCOffline(ctx, d.o.HostID); err != nil {
		d.logf("offline: %v", err)
	}
	audit.Emit(audit.Fact{Kind: audit.HostRCOffline, ActorKind: audit.ActorHarness,
		Data: map[string]string{"reason": "stopped"}})
	d.logf("remote control off")
}

func (d *Daemon) running() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

func (d *Daemon) setOnline(err error) {
	d.mu.Lock()
	d.online = err == nil
	d.lastErr = ""
	if err != nil {
		d.lastErr = err.Error()
	}
	d.mu.Unlock()
}

func (d *Daemon) writeRecord() {
	d.mu.Lock()
	r := Record{
		PID: os.Getpid(), Started: d.started, Beat: time.Now(), HostID: d.o.HostID, URL: d.o.URL,
		Max: d.o.Max, Dirs: d.o.Dirs, LogPath: d.o.LogPath, Online: d.online, Error: d.lastErr,
	}
	for _, c := range d.sessions {
		r.Sessions = append(r.Sessions, c.RunningSession)
	}
	d.mu.Unlock()
	sort.Slice(r.Sessions, func(i, j int) bool { return r.Sessions[i].Started.Before(r.Sessions[j].Started) })
	_ = writeRecord(r)
}

func (d *Daemon) sessionLog(sid string) string {
	dir, err := StateDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sessions", sid+".log")
}

// childArgs is the fixed argv of one `belai rc-session`. The prompt is not in
// it (it goes over stdin), and every value is one the daemon already checked.
func childArgs(c Child) []string {
	args := []string{"rc-session", "-dispatch", c.Dispatch, "-session-id", c.SessionID, "-idle", c.Idle.String()}
	if c.Mode != "" {
		args = append(args, "-mode", c.Mode)
	}
	for _, f := range [][2]string{{"-provider", c.Provider}, {"-model", c.Model}, {"-effort", c.Effort}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	if c.Profile != "" {
		args = append(args, "-profile", c.Profile)
	}
	for _, dir := range c.Dirs {
		args = append(args, "-add-dir", dir)
	}
	if c.GitSync != nil {
		args = append(args, "-git-sync", map[bool]string{true: "on", false: "off"}[*c.GitSync])
	}
	if c.Controls {
		args = append(args, "-controls")
		if c.GuardrailsOff {
			args = append(args, "-allow-guardrails-off")
		}
	}
	if c.Shell {
		args = append(args, "-shell")
	}
	return args
}

// startChild runs `belai rc-session` in the session's directory, in its own
// process group so a stop reaches its tools too. The prompt goes over stdin,
// never argv, so it stays out of the process list.
func startChild(c Child) (int, func() error, error) {
	cmd := exec.Command(c.Exe, childArgs(c)...)
	cmd.Dir = c.Cwd
	cmd.Stdin = strings.NewReader(c.Prompt)
	var logFile *os.File
	if c.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o700); err == nil {
			if f, err := os.OpenFile(c.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				logFile = f
				cmd.Stdout, cmd.Stderr = f, f
			}
		}
	}
	proc.SetProcessGroup(cmd)
	// exec.Command, not CommandContext: the session outlives the dispatch and
	// a stop signals its group through proc.Terminate/Kill. Start refuses a
	// non-nil Cancel on a command CommandContext did not make.
	cmd.Cancel = nil
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return 0, nil, err
	}
	return cmd.Process.Pid, func() error {
		err := cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		return err
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
