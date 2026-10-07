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
	if kind.InstallOnly() {
		return "", fmt.Sprintf("a %s is installed from the library and never backed up from a host", kind)
	}
	name := strings.TrimSpace(r.Name)
	if !libitem.ValidName(kind, name) {
		return "", "that is not a " + string(kind) + " name"
	}
	it, err := libstore.Get(kind, name)
	if errors.Is(err, libstore.ErrNotFound) {
		if kind.Singleton() {
			if held, _, lerr := libstore.List(kind); lerr == nil && len(held) == 1 {
				return "", fmt.Sprintf("this host's %s is named %s, not %s", kind, sanitize.Line(held[0].Name, 64), sanitize.Line(name, 64))
			}
			return "", fmt.Sprintf("this host has no %s configured", kind)
		}
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
// fetchBundleFiles reads the files of a hook bundle the library listed, each by
// its hash and only through the install request that names them. The bounds are
// checked before anything is fetched, and the install checks each file against the
// hash listed.
func (d *Daemon) fetchBundleFiles(ctx context.Context, r sessionsync.Dispatch, refs []sessionsync.FileRef) ([]libstore.BundleFile, string) {
	if len(refs) > libstore.MaxBundleFiles {
		return nil, fmt.Sprintf("refused: the bundle lists %d files; the most is %d", len(refs), libstore.MaxBundleFiles)
	}
	var out []libstore.BundleFile
	for _, f := range refs {
		data, err := d.o.Client.LibraryFetchFile(ctx, d.o.HostID, f.SHA256, r.ID)
		if err != nil {
			return nil, "could not read a file of the hook from the library: " + reason(err.Error())
		}
		out = append(out, libstore.BundleFile{Path: f.Path, Data: data, SHA256: f.SHA256})
	}
	return out, ""
}

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
	opts := libstore.InstallOptions{Overwrite: r.Overwrite, Name: name}
	var res libstore.Result
	if kind == libitem.Hook {
		// A hook bundle: its files are read one by one through the same request.
		files, why := d.fetchBundleFiles(ctx, r, got.Files)
		if why != "" {
			return "", why
		}
		res, err = libstore.InstallBundle(kind, got.Body, files, opts)
	} else {
		res, err = libstore.Install(kind, got.Body, opts)
	}
	switch {
	case errors.Is(err, libstore.ErrExists):
		if kind.Singleton() {
			return "", fmt.Sprintf("this host already has its own %s (named %s); install it again with replace turned on to overwrite the whole configuration", kind, sanitize.Line(libstore.LocalName(kind), 64))
		}
		return "", fmt.Sprintf("this host already has a %s named %s; install it again with replace turned on to overwrite it", kind, sanitize.Line(name, 64))
	case libstore.IsRefusal(err):
		return "", "refused: " + reason(err.Error())
	case err != nil:
		return "", "could not write the " + string(kind) + ": " + reason(err.Error())
	}
	// What the host now exports is what the library holds, so the next check does
	// not take the install for an edit. A hook is never synced, so there is no
	// check to settle.
	if it, err := libstore.Get(kind, name); err == nil && !kind.InstallOnly() {
		d.markSynced(string(kind), it.Name, it.Doc)
	}
	verb := "installed"
	if res.Replaced {
		verb = "installed (replacing the one of that name)"
	}
	return fmt.Sprintf("%s %s %s from version %s", verb, kind, sanitize.Line(name, 64), r.Version), ""
}
