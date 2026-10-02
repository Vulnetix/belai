package rc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The item library (docs/library-items.md), the counterpart of the agent and crew
// libraries. Two requests from the website reach an item on this host, both
// through the dispatch queue and both carrying identifiers only:
//
//	item_backup   export the named item as its canonical document and upload it
//	item_install  read one version of a library item and write it here
//
// A backup writes nothing on this host. An install is a person's action on the
// website, made with their own login, so like a profile install it does not need
// sync.remote_prompts. It is still validated whole (internal/libitem), the text
// of a skill or a prompt passes the sanitisation gate, and an item of the same
// name is replaced only when the request says so. The reason sent back is harness
// text with a short cleaned excerpt, never the document.
//
// Both are refused while the kind's switch (sync.<kinds>) is off, so a user can
// shut the website out of one kind without touching the rest.

// syncItemOn reports whether the user's own settings leave the kind's library
// open (sync.<kinds>). It fails closed.
func syncItemOn(kind libitem.Kind) bool {
	s, err := config.LoadGlobal()
	return err == nil && s.SyncItemEnabled(string(kind))
}

// itemKind reads and checks the kind a request names.
func (d *Daemon) itemKind(r sessionsync.Dispatch) (libitem.Kind, string) {
	kind, ok := libitem.Parse(strings.TrimSpace(r.ItemKind))
	if !ok {
		return "", "that is not a library item kind"
	}
	if !libstore.Supported(kind) {
		return "", fmt.Sprintf("this Belai does not store %s items; update Belai on the host", kind)
	}
	if !d.o.SyncItem(kind) {
		return "", fmt.Sprintf("sync.%s is off on this host, so it takes no %s requests", kind.Segment(), kind)
	}
	return kind, ""
}

// backupItem exports the item the request names and uploads it. It returns the
// report for the acknowledgement, or the reason it refused.
func (d *Daemon) backupItem(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	kind, why := d.itemKind(r)
	if why != "" {
		return "", why
	}
	name := strings.TrimSpace(r.Name)
	if !libitem.ValidName(kind, name) {
		return "", "that is not a " + string(kind) + " name"
	}
	it, err := libstore.Get(kind, name)
	if errors.Is(err, libstore.ErrNotFound) {
		return "", fmt.Sprintf("this host has no %s %s", kind, sanitize.Line(name, 64))
	}
	if err != nil {
		return "", "could not read the " + string(kind) + ": " + reason(err.Error())
	}
	saved, err := d.o.Client.ItemBackup(ctx, d.o.HostID, r.ID, kind, it.Doc)
	if err != nil {
		return "", "the library did not take the " + string(kind) + ": " + reason(err.Error())
	}
	d.markSynced(string(kind), it.Name, it.Doc)
	return fmt.Sprintf("backed up %s %s as version %s", kind, sanitize.Line(name, 64), sanitize.Ident(saved.Version, 12)), ""
}

// installItem reads the library version the request names and writes it on this
// host, or says why it did not.
func (d *Daemon) installItem(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	kind, why := d.itemKind(r)
	if why != "" {
		return "", why
	}
	if !agentprofile.ValidID(r.Library) || !versionPattern.MatchString(r.Version) {
		return "", "that is not a library item and version"
	}
	got, err := d.o.Client.ItemFetch(ctx, d.o.HostID, kind, r.Library, r.Version, r.ID)
	if err != nil {
		return "", "could not read the " + string(kind) + " from the library: " + reason(err.Error())
	}
	name := got.Name
	if name == "" {
		name = strings.TrimSpace(r.Name)
	}
	res, err := libstore.Install(kind, got.Body, libstore.InstallOptions{Overwrite: r.Overwrite, Name: name})
	switch {
	case errors.Is(err, libstore.ErrExists):
		return "", fmt.Sprintf("this host already has a %s named %s; install it again with replace turned on to overwrite it", kind, sanitize.Line(name, 64))
	case libstore.IsRefusal(err):
		return "", "refused: " + reason(err.Error())
	case err != nil:
		return "", "could not write the " + string(kind) + ": " + reason(err.Error())
	}
	// What the host now exports is what the library holds, so the next check does
	// not take the install for an edit.
	if it, err := libstore.Get(kind, name); err == nil {
		d.markSynced(string(kind), it.Name, it.Doc)
	}
	verb := "installed"
	if res.Replaced {
		verb = "installed (replacing the one of that name)"
	}
	return fmt.Sprintf("%s %s %s from version %s", verb, kind, sanitize.Line(name, 64), r.Version), ""
}
