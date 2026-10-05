package rc

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/teleport/changes"
)

// A teleport_code request asks this host, the origin of a teleport, to move the
// changes the session made in a directory it offers: the commits it has not
// pushed and the work it has not committed (docs/teleport.md "Code"). The
// request carries identifiers only: the teleport it serves, whether the target's
// user agreed to a teleport branch, and whether the forge is not to be tried. The
// directory is the session's, and is checked against this host's own list.
//
// The host reads its own working tree, never a path the website names, and
// decides for itself what may leave: a credential file, a binary, an oversized
// file, a link and anything git ignores never travel (internal/teleport/changes).
// It then answers one of three ways, by an upload only this host's request can
// make:
//
//   - a branch: it pushed one belai/teleport/<id> branch, when its own setting
//     (teleport.push) and the target's agreement allow it, by an explicit refspec;
//   - a replay: it sends the patch, with its model's summary and per-file
//     instructions (the teleport_distill role, with a harness fallback), for the
//     target to apply and finish;
//   - none, with the reason, when there is nothing to move or it cannot be read.
//
// The request never fails a teleport: the transcript and the profile move
// regardless, and the target tells the user why the changes did not.

// teleportIDRe is the shape of a teleport id in a request.
var teleportIDRe = regexp.MustCompile(`^[0-9a-f-]{8,36}$`)

// teleportCodeTimeout bounds one request: reading the tree, a push and a
// model's hand-over.
const teleportCodeTimeout = 4 * time.Minute

// teleportCode answers a teleport_code request. It returns a short report for
// the acknowledgement, or the reason it refused.
func (d *Daemon) teleportCode(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if !teleportIDRe.MatchString(r.Teleport) {
		return "", "a teleport code request names a teleport"
	}
	cwd, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "this host does not offer that directory"
	}
	rctx, cancel := context.WithTimeout(ctx, teleportCodeTimeout)
	defer cancel()
	code := d.buildTeleportCode(rctx, cwd, r)
	// The upload outlives the work's own deadline a little: a finished result must not be lost to it.
	uctx, ucancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer ucancel()
	if err := d.o.Client.TeleportCodePut(uctx, d.o.HostID, r.Teleport, r.ID, code); err != nil {
		return "", "the website did not take the changes: " + reason(err.Error())
	}
	return "sent the changes as " + code.Mode, ""
}

// buildTeleportCode reads dir's changes and decides how they travel.
func (d *Daemon) buildTeleportCode(ctx context.Context, dir string, r sessionsync.Dispatch) sessionsync.TeleportCode {
	none := func(why string) sessionsync.TeleportCode {
		return sessionsync.TeleportCode{Mode: sessionsync.CodeNone, Reason: sanitize.Line(why, 240)}
	}
	snap, err := changes.Collect(ctx, d.o.TeleportGit, dir)
	switch {
	case errors.Is(err, changes.ErrNothing):
		return none("the origin session had nothing left to move")
	case errors.Is(err, changes.ErrNoBase):
		return none("the origin's branch has no pushed commit to build on, so its changes cannot be sent; push the branch there")
	case errors.Is(err, changes.ErrTooLarge), errors.Is(err, changes.ErrTooMany):
		return none(err.Error())
	case err != nil:
		return none("the origin host could not read its changes: " + sanitize.Line(err.Error(), 160))
	}

	policy := d.teleportPushPolicy()
	mayPush := policy == config.TeleportPushAllow || (policy == config.TeleportPushAsk && r.Push)
	why := ""
	switch {
	case r.Replay:
		why = "the target host could not fetch the teleport branch"
	case policy == config.TeleportPushNever:
		why = "the origin host does not push a teleport branch (teleport.push is never there)"
	case !mayPush:
		why = "the origin host was not asked to push a teleport branch (pass -teleport-push, or set teleport.push to allow there)"
	}
	if !r.Replay && mayPush {
		pushed, perr := changes.Push(ctx, d.o.TeleportGit, dir, snap, r.Teleport)
		if perr == nil {
			d.logf("teleport_code: pushed %s", pushed.Branch)
			return sessionsync.TeleportCode{
				Mode: sessionsync.CodeBranch, Branch: pushed.Branch, Commit: pushed.Commit, Base: snap.Base, Head: snap.Head, Tree: snap.Tree,
			}
		}
		why = "the origin host could not push a teleport branch: " + sanitize.Line(perr.Error(), 160)
		d.logf("teleport_code: %s", why)
	}

	files := make([]rolemanager.TeleportFile, 0, len(snap.Files))
	for _, f := range snap.Files {
		files = append(files, rolemanager.TeleportFile{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed})
	}
	skipped := make([]string, 0, len(snap.Skipped))
	for _, s := range snap.Skipped {
		skipped = append(skipped, s.Path+" ("+s.Reason+")")
	}
	var hand rolemanager.Distilled
	if d.o.Distill != nil {
		hand = d.o.Distill(ctx, files, skipped, snap.Patch)
	} else {
		hand = rolemanager.ComposeTeleportFallback(files)
	}
	out := sessionsync.TeleportCode{
		Mode: sessionsync.CodeReplay, Reason: sanitize.Line(why, 240), Base: snap.Base, Head: snap.Head, Tree: snap.Tree,
		Patch: snap.Patch, Summary: hand.Summary, Instructions: hand.Instructions,
	}
	for _, f := range snap.Files {
		out.Files = append(out.Files, sessionsync.TeleportCodeFile{Path: f.Path, Status: f.Status, Added: f.Added, Removed: f.Removed})
	}
	for _, s := range snap.Skipped {
		out.Skipped = append(out.Skipped, sessionsync.TeleportCodeSkip{Path: s.Path, Reason: s.Reason})
	}
	d.logf("teleport_code: replay of %d file%s (%s)", len(out.Files), map[bool]string{true: "", false: "s"}[len(out.Files) == 1], strings.TrimSpace(map[bool]string{true: "model summary", false: "harness summary"}[hand.FromModel]))
	return out
}

func (d *Daemon) teleportPushPolicy() string {
	if d.o.TeleportPush == nil {
		return config.TeleportPushAsk
	}
	switch p := d.o.TeleportPush(); p {
	case config.TeleportPushAllow, config.TeleportPushAsk, config.TeleportPushNever:
		return p
	}
	return config.TeleportPushNever
}

// startTeleportCode starts a teleport_code request in the background, so a slow
// model never holds up the queue. At most two run at once; another meanwhile is
// refused with the reason.
func (d *Daemon) startTeleportCode(ctx context.Context, r sessionsync.Dispatch, ack func(ctx context.Context, id, status, sid, reason string)) {
	select {
	case d.codeSlot <- struct{}{}:
	default:
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "this host is already moving the changes of two teleports; try again in a minute")
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		report, why := d.teleportCode(ctx, r)
		<-d.codeSlot
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if why != "" {
			d.logf("refused teleport_code: %s", why)
			ack(actx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("teleport_code: %s", report)
		ack(actx, r.ID, sessionsync.DispatchStarted, "", report)
	}()
}
