package rc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/libscan"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Two requests from the website let a person pull items from other agent
// harnesses into the library:
//
//	library_scan    search this host for items and upload a report
//	library_import  read one item the report named and upload its canonical document
//
// Both are always on: there is no sync.* switch, because neither reads anything
// unless a person asks on the website, a scan sends only names, paths, hashes,
// verdicts and short notes (internal/libscan), and a document leaves only when
// that person imports that item. A request carries identifiers only. An import
// names a path the scan reported, but the host does not trust it: it re-derives
// where it may read from its own harness list and trusted repositories, refuses a
// path outside them or reached through a link, reads the file itself, and refuses
// an item whose canonical bytes no longer hash to what the scan saw.

// scanBudget is how long a scan may search; what it has found by then is
// reported, flagged partial. The website gives a host 30 seconds from delivery.
var scanBudget = libscan.DefaultBudget

// ackTimeout bounds the upload and the acknowledgement after the work is done,
// which outlive the scan's own deadline.
const ackTimeout = 40 * time.Second

var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ackFunc = func(ctx context.Context, id, status, sid, reason string)

// startLibraryScan checks a scan request and runs it in the background, so a
// walk of the disk never holds up the queue. One scan runs at a time.
func (d *Daemon) startLibraryScan(ctx context.Context, r sessionsync.Dispatch, ack ackFunc) {
	kinds, err := libscan.KindSet(r.ItemKind)
	if err != nil {
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", reason(err.Error()))
		return
	}
	select {
	case d.scanSlot <- struct{}{}:
	default:
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "this host is already scanning; try again in a moment")
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		sctx, cancel := context.WithTimeout(ctx, scanBudget)
		rep := libscan.Scan(sctx, libscan.Options{Kinds: kinds})
		cancel()
		<-d.scanSlot
		actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
		defer acancel()
		if err := d.o.Client.PostLibraryScan(actx, d.o.HostID, r.ID, rep); err != nil {
			why := "the library did not take the scan report: " + reason(err.Error())
			d.logf("refused library_scan: %s", why)
			ack(actx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("library_scan: %s", rep.Summary())
		ack(actx, r.ID, sessionsync.DispatchStarted, "", rep.Summary())
	}()
}

// startLibraryImport runs an import in the background. A selection on the
// website sends one request per item, so imports queue behind each other.
func (d *Daemon) startLibraryImport(ctx context.Context, r sessionsync.Dispatch, ack ackFunc) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.importMu.Lock()
		defer d.importMu.Unlock()
		ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
		defer cancel()
		report, why := d.libraryImport(ictx, r)
		if why != "" {
			d.logf("refused library_import %s %s: %s", sanitizeName(r.ItemKind), short(r.ID), why)
			ack(ictx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("library_import: %s", report)
		ack(ictx, r.ID, sessionsync.DispatchStarted, "", report)
	}()
}

// libraryImport reads the item the request names and uploads it. It returns the
// report for the acknowledgement, or the reason it refused.
func (d *Daemon) libraryImport(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	kind, ok := agentimport.ParseKind(strings.TrimSpace(r.ItemKind))
	if !ok {
		return "", "that is not an item kind"
	}
	switch {
	case !hexSHA.MatchString(r.SHA256):
		return "", "the request carries no item hash"
	case r.Path == "" || len(r.Path) > libscan.MaxPathBytes:
		return "", "the request names no path"
	}
	target := strings.TrimSpace(r.Target)
	if kind == agentimport.KindDocument {
		if target == "" {
			return "", "name the agent that receives the document"
		}
		if len(target) > 128 || sanitize.Line(target, 128) != target {
			return "", "that is not an agent name"
		}
	} else {
		target = ""
	}
	home, err := libscan.DefaultHome()
	if err != nil {
		return "", "could not find the home directory"
	}
	roots := libscan.Roots(home, libscan.TrustedRepos(), nil)
	res, err := libscan.Resolve(roots, kind, r.Path)
	if err != nil {
		return "", reason(err.Error())
	}
	item, err := agentimport.ImportItem(res.File, kind, res.Root.Format, agentimport.Options{Name: res.Hint})
	switch {
	case errors.Is(err, agentimport.ErrSkipped):
		return "", "that file is a link, a credential file or too large, so it is not read"
	case err != nil:
		return "", "refused: " + reason(err.Error())
	case item.SHA256 != r.SHA256:
		return "", "that item changed since the scan; scan again"
	}
	body, err := sessionsync.ImportBody(kind.Markdown(), item.Doc)
	if err != nil {
		return "", reason(err.Error())
	}
	up := sessionsync.LibraryImport{Dispatch: r.ID, Kind: string(kind), Name: item.Name, Body: body, Target: target}
	for _, sk := range item.Skills {
		up.Skills = append(up.Skills, sessionsync.LibraryImportSkill{Name: sk.Name, Body: string(sk.Doc)})
	}
	saved, err := d.o.Client.PostLibraryImport(ctx, d.o.HostID, up)
	if err != nil {
		return "", "the library did not take the " + string(kind) + ": " + reason(err.Error())
	}
	return fmt.Sprintf("imported %s %s from %s as version %s", kind, sanitize.Line(item.Name, 64), sanitize.Ident(res.Root.Harness, 40), sanitize.Ident(saved.Version, 12)), ""
}
