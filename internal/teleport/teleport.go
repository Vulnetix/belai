// Package teleport continues a session of the account on this host.
//
// `belai -teleport <id>` asks the backend for a teleport, waits for it, reads
// the session's transcript and any agent profile and crew this host lacks, makes
// sure the repository is the right one and at the right commit, writes a new
// session under a new id, and tells the backend what it opened. The session on
// the origin carries on untouched, and running it again makes another session.
//
// Only the transcript and the profiles move. No provider, credential or
// setting comes with them; the session's own mode, active profile and model are
// hints the target applies only where it can. Everything read from the backend
// is untrusted text from another host's session: the transcript is verified
// line by line and cleaned, a profile or crew is validated whole by the library
// installer, and a git ref goes to git only after it passes a shape check.
// docs/teleport.md has the whole sequence.
package teleport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/libinstall"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/version"
)

// API is the backend half of a teleport. *sessionsync.Client is the real one.
type API interface {
	PutHost(ctx context.Context, hostID string, h sessionsync.Host) error
	TeleportCreate(ctx context.Context, hostID, ref string) (sessionsync.Teleport, error)
	TeleportGet(ctx context.Context, hostID, tid string) (sessionsync.TeleportState, error)
	TeleportEntries(ctx context.Context, hostID, tid string, after int64) ([]sessionsync.Entry, error)
	TeleportProfile(ctx context.Context, hostID, tid, profileID, version string) (string, []sessionsync.FileRef, error)
	TeleportCrew(ctx context.Context, hostID, tid, crewID, version string) (sessionsync.CrewFetched, error)
	TeleportFile(ctx context.Context, hostID, tid, sha string) ([]byte, error)
	TeleportAck(ctx context.Context, hostID, tid, status, sessionID, reason string) error
}

// Options is one teleport.
type Options struct {
	// Ref is the session id, or a unique prefix of one, from the command line.
	Ref string
	// RefOverride is -teleport-ref: a ref or commit to check out instead of the
	// one the origin session was at.
	RefOverride string
	// Workdir is where the command ran. It must be inside a trusted git checkout.
	Workdir string
	API     API
	HostID  string
	Host    sessionsync.Host
	Store   *session.Store

	// Trust records that dir, a worktree this teleport made inside a repository
	// the user already trusted, is trusted too. Nil trusts nothing.
	Trust func(dir string) error
	// Progress receives one line at each step. Nil is silent.
	Progress func(string)

	// The rest default to the real thing and exist so tests need no network,
	// clock or git.
	Git          forge.Runner
	WorktreesDir string
	Poll         time.Duration
	Wait         time.Duration
	NewID        func() string
}

// Result is a teleported session, ready for the TUI to resume.
type Result struct {
	// Workdir is the directory the session runs in: Options.Workdir, or the
	// worktree.
	Workdir string
	Key     session.Key
	// SessionID is the new session's id, and OriginID the session it continues.
	SessionID  string
	OriginID   string
	TeleportID string
	// Notices are one-line facts for the user to read when the session opens.
	Notices []string
}

const (
	defaultPoll = time.Second
	defaultWait = 150 * time.Second
	ackTries    = 3
)

// Run teleports the session. On any failure after the backend was asked it
// tells the backend the teleport was refused, and removes what it created.
func Run(ctx context.Context, o Options) (res Result, err error) {
	o = o.withDefaults()
	say := o.say
	if o.Ref == "" || o.API == nil || o.Store == nil || o.HostID == "" {
		return res, errors.New("teleport: missing session id, backend, store or host id")
	}
	// Check the directory before asking the backend for anything, so a user in
	// the wrong place hears it at once and nothing is requested.
	newID := o.NewID()
	p := plan{workdir: o.Workdir, override: o.RefOverride, newID: newID, run: o.Git, worktree: o.WorktreesDir, progress: say}
	if err := p.requireRepo(); err != nil {
		return res, err
	}

	say("asking the backend to teleport " + sanitize.Line(o.Ref, 40))
	if err := o.API.PutHost(ctx, o.HostID, o.Host); err != nil {
		return res, fmt.Errorf("register this host: %w", reason(err))
	}
	tp, err := o.API.TeleportCreate(ctx, o.HostID, o.Ref)
	if err != nil {
		return res, reason(err)
	}
	res.TeleportID, res.OriginID = tp.ID, tp.OriginSessionID

	var made madeThings
	defer func() {
		if err == nil {
			return
		}
		o.undo(&made, p)
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = o.API.TeleportAck(actx, o.HostID, tp.ID, "refused", "", err.Error())
	}()

	state, err := o.await(ctx, tp)
	if err != nil {
		return res, err
	}
	og := parseOriginGit(state.Git)

	say("reading the transcript")
	entries, err := o.readEntries(ctx, tp.ID, state.Teleport.SnapshotSeq)
	if err != nil {
		return res, err
	}
	if err := verify(entries, state.Teleport.SnapshotSeq); err != nil {
		return res, err
	}

	co, err := p.prepare(ctx, og)
	if err != nil {
		return res, err
	}
	if co.Worktree {
		made.worktree, made.root = co.Dir, co.Root
		say("created a worktree at the commit the session was at")
	}
	res.Notices = append(res.Notices, co.Notices...)

	res.Notices = append(res.Notices, o.install(ctx, tp.ID, state.Manifest)...)

	key, err := session.KeyFor(co.Dir)
	if err != nil {
		return res, err
	}
	built, _, err := build(entries, session.Meta{
		Schema: session.SchemaVersion, Cwd: co.Dir, Version: version.Version, TeleportedFrom: tp.OriginSessionID,
	}, func(name string) bool {
		_, err := agentprofile.Load(name)
		return err == nil
	})
	if err != nil {
		return res, err
	}
	if err := o.Store.Import(key, newID, built); err != nil {
		return res, err
	}
	made.session, made.key = newID, key

	if co.Worktree && o.Trust != nil {
		if err := o.Trust(co.Dir); err != nil {
			return res, fmt.Errorf("trust the worktree: %w", err)
		}
	}
	if err := o.ack(ctx, tp.ID, newID); err != nil {
		return res, err
	}
	say("opened session " + newID[:8])
	res.Workdir, res.Key, res.SessionID = co.Dir, key, newID
	return res, nil
}

func (o Options) withDefaults() Options {
	if o.Git == nil {
		o.Git = forge.HardenedGit("", "")
	}
	if o.WorktreesDir == "" {
		if dir, err := config.WorktreesDir(); err == nil {
			o.WorktreesDir = dir
		}
	}
	if o.Poll <= 0 {
		o.Poll = defaultPoll
	}
	if o.Wait <= 0 {
		o.Wait = defaultWait
	}
	if o.NewID == nil {
		o.NewID = session.MustID
	}
	return o
}

func (o Options) say(s string) {
	if o.Progress != nil {
		o.Progress(s)
	}
}

// reason turns a backend error into the text the user reads: the backend's own
// explanation when it gave one, already cleaned.
func reason(err error) error {
	var te *sessionsync.TeleportError
	if errors.As(err, &te) {
		switch te.Status {
		case 404:
			return errors.New("the backend has no session by that id on this account (sessions are mirrored only while sync is on)")
		case 401:
			return errors.New("the backend did not accept this host's Vulnetix login; run vulnetix auth login")
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("could not reach the backend: %s", sanitize.Line(ue.Err.Error(), 120))
	}
	return err
}

// madeThings is what a teleport created, for undo.
type madeThings struct {
	worktree, root string
	session        string
	key            session.Key
}

func (o Options) undo(m *madeThings, p plan) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if m.session != "" {
		_ = o.Store.Remove(m.key, m.session)
	}
	if m.worktree != "" {
		p.remove(ctx, m.root, m.worktree)
	}
}

// await waits for the teleport to be ready and returns its state. The backend
// may first ask the origin host to back up its agent profile, which takes as
// long as that host takes to answer.
func (o Options) await(ctx context.Context, tp sessionsync.Teleport) (sessionsync.TeleportState, error) {
	deadline := time.Now().Add(o.Wait)
	cur := tp
	told := false
	for {
		switch cur.Status {
		case sessionsync.TeleportReady:
			// The state a ready teleport carries (git facts, manifest) comes
			// only from a read.
			st, err := o.API.TeleportGet(ctx, o.HostID, tp.ID)
			if err != nil {
				return st, reason(err)
			}
			if st.Teleport.Status == sessionsync.TeleportReady {
				return st, nil
			}
			cur = st.Teleport
			continue
		case sessionsync.TeleportFailed:
			why := cur.Reason
			if why == "" {
				why = "the backend could not set the teleport up"
			}
			return sessionsync.TeleportState{}, errors.New(why)
		case sessionsync.TeleportExpired:
			return sessionsync.TeleportState{}, errors.New("the teleport took too long and expired; run it again")
		case sessionsync.TeleportCompleted:
			return sessionsync.TeleportState{}, errors.New("that teleport was already used; run it again for a new session")
		}
		if cur.Status == sessionsync.TeleportBackingUp && !told {
			o.say("waiting for the origin host to back up its agent profile")
			told = true
		}
		if time.Now().After(deadline) {
			return sessionsync.TeleportState{}, errors.New("the origin host did not answer in time; is belai rc running there?")
		}
		select {
		case <-ctx.Done():
			return sessionsync.TeleportState{}, ctx.Err()
		case <-time.After(o.Poll):
		}
		st, err := o.API.TeleportGet(ctx, o.HostID, tp.ID)
		if err != nil {
			return st, reason(err)
		}
		cur = st.Teleport
	}
}

// readEntries pages the transcript up to the snapshot.
func (o Options) readEntries(ctx context.Context, tid string, snapshot int64) ([]sessionsync.Entry, error) {
	var out []sessionsync.Entry
	after := int64(-1)
	for after < snapshot {
		page, err := o.API.TeleportEntries(ctx, o.HostID, tid, after)
		if err != nil {
			return nil, reason(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.Seq <= after {
				return nil, errors.New("the backend sent the transcript out of order")
			}
			if e.Seq > snapshot {
				return out, nil
			}
			out = append(out, e)
			after = e.Seq
		}
		if len(out) > maxEntries {
			return nil, errors.New("the transcript is longer than a teleport takes")
		}
	}
	return out, nil
}

func (o Options) ack(ctx context.Context, tid, newID string) error {
	var err error
	for i := 0; i < ackTries; i++ {
		if err = o.API.TeleportAck(ctx, o.HostID, tid, "completed", newID, ""); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}
	return fmt.Errorf("the backend would not record the teleport, so the new session was not kept: %w", reason(err))
}

// install puts the profile and crews the manifest names on this host when it
// lacks them, and returns a notice for each thing it could not. A profile that
// cannot be installed never fails the teleport: the session opens under the
// default agent and says so.
func (o Options) install(ctx context.Context, tid string, m sessionsync.TeleportManifest) []string {
	var notes []string
	if m.Profile != "" && len(m.Profiles) == 0 {
		return []string{"the agent profile " + sanitize.Line(m.Profile, 60) + " is not in the library, so the session continues under the default agent"}
	}
	in := libinstall.Installer{Src: source{o: o, tid: tid}}
	for _, mem := range m.Profiles {
		if _, err := agentprofile.Load(mem.Profile); err == nil {
			continue
		}
		o.say("installing the agent profile " + sanitize.Line(mem.Profile, 60))
		if _, _, why := in.Profile(ctx, mem.Library, mem.Version, false); why != "" {
			notes = append(notes, "could not install the agent profile "+sanitize.Line(mem.Profile, 60)+": "+why)
		}
	}
	for _, c := range m.Crews {
		if _, ok := libinstall.StoredCrew(c.Name); ok {
			continue
		}
		o.say("installing the crew " + sanitize.Line(c.Name, 60))
		if _, why := in.Crew(ctx, c.ID, c.Version, false); why != "" {
			notes = append(notes, "could not install the crew "+sanitize.Line(c.Name, 60)+": "+why)
		}
	}
	if m.Profile != "" {
		if _, err := agentprofile.Load(m.Profile); err != nil {
			notes = append(notes, "the agent profile "+sanitize.Line(m.Profile, 60)+" is not installed here, so the session continues under the default agent")
		}
	}
	return notes
}

// source reads library versions through this teleport's gated routes.
type source struct {
	o   Options
	tid string
}

func (s source) Profile(ctx context.Context, library, ver string) (string, []sessionsync.FileRef, error) {
	md, files, err := s.o.API.TeleportProfile(ctx, s.o.HostID, s.tid, library, ver)
	if err != nil {
		return "", nil, err
	}
	// A scheduled profile is left behind: a teleport carries no schedule, and a
	// profile that only means something on a schedule has no place in a session
	// someone continues by hand.
	if p, err := agentprofile.ParseMarkdown([]byte(md)); err == nil && (p.Mode == agentprofile.ModeScheduled || strings.TrimSpace(p.Schedule) != "") {
		return "", nil, errors.New("scheduled agents are not teleported")
	}
	return md, files, nil
}

func (s source) File(ctx context.Context, sha string) ([]byte, error) {
	return s.o.API.TeleportFile(ctx, s.o.HostID, s.tid, sha)
}

func (s source) Crew(ctx context.Context, library, ver string) (sessionsync.CrewFetched, error) {
	return s.o.API.TeleportCrew(ctx, s.o.HostID, s.tid, library, ver)
}
