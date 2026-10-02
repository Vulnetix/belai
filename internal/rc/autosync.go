package rc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Automatic sync of profiles, crews and library items (setting sync.profiles for
// agents and crews, one sync.<kinds> setting per kind of item, all default on).
// Backups and installs stay requests the website makes; this keeps the library
// current without a click. Every DefaultLibrarySyncEvery the daemon hashes each
// stored profile, crew and item and asks the server what to do with the ones it
// has not settled yet. The server answers per item:
//
//	push      the library has nothing newer: the host pushes this copy
//	current   the library already holds it
//	diverged  the website saved a version this host has not installed, so the
//	          host's copy must not win; the person decides with a backup or an
//	          install
//	skip      deleted from the library, or not this account's
//
// Only the profile markdown, the crew JSON and the item documents travel here.
// A profile file's contents leave the host only for a profile_backup request, so
// a person always chooses to upload them, and the Agents page says when a library
// copy has none. The state below is in memory: a restart asks again and the
// server answers again.

// DefaultLibrarySyncEvery is how often the host's profiles and crews are checked.
const DefaultLibrarySyncEvery = 30 * time.Second

// settledFor is how long a diverged or skipped answer is trusted before the host
// asks again, in case the person resolved it.
const settledFor = 10 * time.Minute

// unsupportedFor is how long the sync waits before asking a website that answered
// not found: one that predates the library sync, or that does not know this host.
const unsupportedFor = 10 * time.Minute

// syncBatch is how many items one request asks about.
const syncBatch = 100

// LibraryRemote is the server half of the sync (the sync client unless a test
// replaces it).
type LibraryRemote interface {
	LibrarySyncAll(ctx context.Context, hostID string, agents, crews []sessionsync.SyncItem, items []sessionsync.SyncItemRef) (agentAnswers, crewAnswers []sessionsync.SyncAnswer, itemAnswers []sessionsync.SyncItemAnswer, err error)
	LibrarySyncProfile(ctx context.Context, hostID, profileID, markdown string) (string, error)
	LibrarySyncCrew(ctx context.Context, hostID, crewID string, crew json.RawMessage) (string, error)
	LibrarySyncItem(ctx context.Context, hostID string, kind libitem.Kind, name string, body []byte) (string, error)
}

type syncRecord struct {
	hash   string
	action string
	at     time.Time
}

type libSyncState struct {
	mu      sync.Mutex
	records map[string]syncRecord
	lastErr string
	// pausedUntil is when a website that does not know the sync routes is asked
	// again, so an older server is not asked every 30 seconds.
	pausedUntil time.Time
}

func syncKey(kind, id string) string { return kind + ":" + id }

func hashOf(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// markSynced records that the library holds data for the item, so the next
// check does not ask about it: after a backup, an install, or a "current" answer.
func (d *Daemon) markSynced(kind, id string, data []byte) {
	d.libsync.mu.Lock()
	defer d.libsync.mu.Unlock()
	d.libsync.records[syncKey(kind, id)] = syncRecord{hash: hashOf(data), action: sessionsync.SyncCurrent, at: time.Now()}
}

// localItem is one profile, crew or library item on this host, ready to hash and
// push. kind is "agent", "crew" or a library item kind (skill, prompt, ...); an
// item has no id, so its name stands in.
type localItem struct {
	kind, id, name string
	data           []byte
}

// isLibraryItem reports whether the item is one of the item kinds rather than an
// agent or a crew.
func (it localItem) isLibraryItem() bool { return it.kind != "agent" && it.kind != "crew" }

// itemKindsOn lists the kinds of library item whose sync switch is on and that
// this build can store.
func (d *Daemon) itemKindsOn() []libitem.Kind {
	var out []libitem.Kind
	for _, k := range libstore.Kinds() {
		if d.o.SyncItem(k) {
			out = append(out, k)
		}
	}
	return out
}

// localLibraryItems reads what this host holds of each kind. An item the library
// cannot take is left out, and a kind holding more than the library keeps (200)
// is cut at the first 200 by name.
func localLibraryItems(kinds []libitem.Kind) []localItem {
	var out []localItem
	for _, k := range kinds {
		items, _, err := libstore.List(k)
		if err != nil {
			continue
		}
		if len(items) > libitem.MaxItemsPerKind {
			items = items[:libitem.MaxItemsPerKind]
		}
		for _, it := range items {
			out = append(out, localItem{kind: string(k), id: it.Name, name: it.Name, data: it.Doc})
		}
	}
	return out
}

// localItems reads every stored profile and crew that has an id and fits the
// library's size limits, and every item of the kinds that are switched on.
// Built-in and plugin profiles are never synced.
func localItems(agents bool, kinds []libitem.Kind) []localItem {
	out := localLibraryItems(kinds)
	if !agents {
		return out
	}
	if list, err := agentprofile.List(); err == nil {
		for _, p := range list {
			if p.Builtin || p.File == "" || !agentprofile.ValidID(p.ID) {
				continue
			}
			md, err := agentprofile.MarshalMarkdown(p)
			if err != nil || len(md) > sessionsync.MaxLibraryProfile {
				continue
			}
			out = append(out, localItem{kind: "agent", id: p.ID, name: p.Name, data: md})
		}
	}
	for _, c := range agentprofile.StoredCrews() {
		if !agentprofile.ValidID(c.ID) {
			continue
		}
		js, err := c.CanonicalJSON()
		if err != nil || len(js) > sessionsync.MaxLibraryCrew {
			continue
		}
		out = append(out, localItem{kind: "crew", id: c.ID, name: c.Name, data: js})
	}
	return out
}

// due reports whether an item needs asking about: it changed since the host last
// settled it, or a diverged or skipped answer has gone stale.
func (s *libSyncState) due(it localItem, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[syncKey(it.kind, it.id)]
	if !ok || rec.hash != hashOf(it.data) {
		return true
	}
	switch rec.action {
	case sessionsync.SyncDiverged, sessionsync.SyncSkip:
		return now.Sub(rec.at) >= settledFor
	}
	return false
}

func (s *libSyncState) set(it localItem, action string, now time.Time) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := syncKey(it.kind, it.id)
	prev, had := s.records[k]
	s.records[k] = syncRecord{hash: hashOf(it.data), action: action, at: now}
	return !had || prev.action != action || prev.hash != hashOf(it.data)
}

// librarySync runs the automatic sync until ctx ends.
func (d *Daemon) librarySync(ctx context.Context) {
	defer d.wg.Done()
	// The first check comes soon after start, so a host that was offline picks
	// up where it left off.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		d.syncLibrary(ctx)
		timer.Reset(d.o.LibrarySyncEvery)
	}
}

// syncLibrary checks the host's profiles, crews and items once.
func (d *Daemon) syncLibrary(ctx context.Context) {
	agents := d.o.SyncProfiles()
	kinds := d.itemKindsOn()
	if !agents && len(kinds) == 0 {
		return
	}
	if d.libsync.paused(d.now()) {
		return
	}
	if agents {
		// A profile or crew file written by hand has no id until it is given one.
		_, _ = agentprofile.EnsureIDs()
		_, _ = agentprofile.EnsureCrewIDs()
	}

	now := d.now()
	var due []localItem
	for _, it := range localItems(agents, kinds) {
		if d.libsync.due(it, now) {
			due = append(due, it)
		}
	}
	for len(due) > 0 {
		n := min(len(due), syncBatch)
		if err := d.syncBatch(ctx, due[:n], now); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, sessionsync.ErrNotFound) {
				d.syncUnsupported(now)

				return
			}
			d.syncFailed(err)
			return
		}
		due = due[n:]
	}
	d.syncRecovered()
}

func (d *Daemon) syncBatch(ctx context.Context, batch []localItem, now time.Time) error {
	byKey := map[string]localItem{}
	var agents, crews []sessionsync.SyncItem
	var items []sessionsync.SyncItemRef
	for _, it := range batch {
		byKey[syncKey(it.kind, it.id)] = it
		switch {
		case it.kind == "agent":
			agents = append(agents, sessionsync.SyncItem{ID: it.id, SHA256: hashOf(it.data)})
		case it.kind == "crew":
			crews = append(crews, sessionsync.SyncItem{ID: it.id, SHA256: hashOf(it.data)})
		default:
			items = append(items, sessionsync.SyncItemRef{Kind: it.kind, Name: it.name, SHA256: hashOf(it.data)})
		}
	}
	agentAns, crewAns, itemAns, err := d.o.LibraryRemote.LibrarySyncAll(ctx, d.o.HostID, agents, crews, items)
	if err != nil {
		return err
	}
	answered := map[string]bool{}
	apply := func(kind, id, action string) error {
		it, ok := byKey[syncKey(kind, id)]
		if !ok {
			return nil
		}
		answered[syncKey(kind, id)] = true
		switch action {
		case sessionsync.SyncPush:
			return d.syncPush(ctx, it, now)
		case sessionsync.SyncCurrent:
			d.libsync.set(it, sessionsync.SyncCurrent, now)
		case sessionsync.SyncDiverged:
			if d.libsync.set(it, sessionsync.SyncDiverged, now) {
				d.logf("library: %s %s differs from the website's copy, which is newer; back it up or install it from the website to settle it", kind, sanitize.Ident(it.name, 64))
			}
		default:
			d.libsync.set(it, sessionsync.SyncSkip, now)
		}
		return nil
	}
	for _, a := range agentAns {
		if err := apply("agent", a.ID, a.Action); err != nil {
			return err
		}
	}
	for _, a := range crewAns {
		if err := apply("crew", a.ID, a.Action); err != nil {
			return err
		}
	}
	for _, a := range itemAns {
		if err := apply(a.Kind, a.Name, a.Action); err != nil {
			return err
		}
	}
	// An item the server did not answer (a website that predates items) is settled
	// as skipped, so it is asked about again in ten minutes rather than every check.
	for k, it := range byKey {
		if it.isLibraryItem() && !answered[k] {
			d.libsync.set(it, sessionsync.SyncSkip, now)
		}
	}
	return nil
}

// syncPush pushes one item the server said to push. A library that moved in the
// meantime answers a conflict, which settles the item as diverged.
func (d *Daemon) syncPush(ctx context.Context, it localItem, now time.Time) error {
	var version string
	var err error
	switch {
	case it.kind == "agent":
		version, err = d.o.LibraryRemote.LibrarySyncProfile(ctx, d.o.HostID, it.id, string(it.data))
	case it.kind == "crew":
		version, err = d.o.LibraryRemote.LibrarySyncCrew(ctx, d.o.HostID, it.id, it.data)
	default:
		version, err = d.o.LibraryRemote.LibrarySyncItem(ctx, d.o.HostID, libitem.Kind(it.kind), it.name, it.data)
	}
	switch {
	case errors.Is(err, sessionsync.ErrConflict):
		d.libsync.set(it, sessionsync.SyncDiverged, now)
		return nil
	case err != nil:
		// This item is refused (a crew the library will not take); the rest go on.
		if ctx.Err() == nil && !errors.Is(err, sessionsync.ErrUnauthorized) && !errors.Is(err, sessionsync.ErrNotFound) {
			if d.libsync.set(it, sessionsync.SyncSkip, now) {
				d.logf("library: %s %s was not taken: %s", it.kind, sanitize.Ident(it.name, 64), reason(err.Error()))
			}
			return nil
		}
		return err
	}
	d.libsync.set(it, sessionsync.SyncCurrent, now)
	d.logf("library: synced %s %s as version %s", it.kind, sanitize.Ident(it.name, 64), sanitize.Ident(version, 12))
	return nil
}

// paused reports whether the sync is waiting out a website that did not know its routes.
func (s *libSyncState) paused(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return now.Before(s.pausedUntil)
}

// syncUnsupported handles a not-found answer to the sync request: the website
// predates the automatic sync (or does not know this host). It says so once, in
// plain words, and asks again in ten minutes instead of every check. Backups and
// installs that the website asks for still work.
func (d *Daemon) syncUnsupported(now time.Time) {
	d.libsync.mu.Lock()
	d.libsync.pausedUntil = now.Add(unsupportedFor)
	same := d.libsync.lastErr == "unsupported"
	d.libsync.lastErr = "unsupported"
	d.libsync.mu.Unlock()
	if !same {
		d.logf("library sync: the website does not take automatic sync yet (or does not know this host), so profiles, crews and items are backed up only when the website asks; checking again in %s. Set sync.profiles and the sync.<kind> settings to false to stop checking", unsupportedFor)
	}
}

// syncFailed logs a failed check once until one succeeds.
func (d *Daemon) syncFailed(err error) {
	msg := reason(err.Error())
	d.libsync.mu.Lock()
	same := d.libsync.lastErr == msg
	d.libsync.lastErr = msg
	d.libsync.mu.Unlock()
	if !same {
		d.logf("library sync: %s (it retries)", msg)
	}
}

func (d *Daemon) syncRecovered() {
	d.libsync.mu.Lock()
	had := d.libsync.lastErr != ""
	d.libsync.lastErr = ""
	d.libsync.mu.Unlock()
	if had {
		d.logf("library sync: working again")
	}
}
