package rc

import (
	"context"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/avatar"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/svgguard"
)

// An avatar request asks this host to redraw Pix for an agent that is being
// created on the website, with this host's main model. The request carries one
// identifier, the agent creator's id. The daemon reads the display name,
// palette and personality from the website, draws, and posts back only the SVG
// (or why it did not). The website's text is a web prompt: it is cleaned and
// checked by the security classifier before a model sees it, and what comes
// back is admitted by internal/svgguard before it leaves, so the website never
// receives a model's text, only an image the guard wrote.

// avatarTimeout bounds one drawing. The website expires the request later.
var avatarTimeout = 100 * time.Second

// startAvatar checks an avatar request and starts it in the background, so a
// slow model never holds up the queue. Only one drawing runs at a time; another
// request meanwhile is refused with the reason.
func (d *Daemon) startAvatar(ctx context.Context, r sessionsync.Dispatch, ack func(ctx context.Context, id, status, sid, reason string)) {
	switch {
	case !d.o.RemotePrompts():
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "sync.remote_prompts is off on this host, so the website cannot ask it to draw")
		return
	case !agentprofile.ValidID(r.Creator):
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "that is not an agent creator id")
		return
	}
	select {
	case d.avatarSlot <- struct{}{}:
	default:
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", "this host is already drawing an avatar; try again in a minute")
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		// The slot is free the moment the drawing is done, before the website
		// is told: a request sent right after the acknowledgement must not be
		// refused for a drawing that has already finished.
		var free sync.Once
		release := func() { free.Do(func() { <-d.avatarSlot }) }
		defer release()
		dctx, cancel := context.WithTimeout(ctx, avatarTimeout)
		defer cancel()
		why := d.drawAvatar(dctx, r)
		release()
		// The acknowledgement outlives the drawing's own deadline.
		actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer acancel()
		if why != "" {
			d.logf("refused avatar %s: %s", short(r.Creator), why)
			ack(actx, r.ID, sessionsync.DispatchRefused, "", why)
			return
		}
		d.logf("drew an avatar for %s", short(r.Creator))
		ack(actx, r.ID, sessionsync.DispatchStarted, "", "drew the avatar")
	}()
}

// drawAvatar reads the request, draws and posts the result. It returns the
// reason it failed, which is harness text, or "".
func (d *Daemon) drawAvatar(ctx context.Context, r sessionsync.Dispatch) string {
	cr, err := d.o.Client.CreatorFetch(ctx, d.o.HostID, r.Creator, r.ID)
	if err != nil {
		return "could not read the avatar request: " + reason(err.Error())
	}
	// post answers the request. A drawing that cannot be posted is lost, so a
	// failed post is the reason rather than a silent success.
	post := func(svg, why string) string {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if err := d.o.Client.CreatorAvatar(pctx, d.o.HostID, r.Creator, r.ID, svg, why); err != nil {
			return "the website did not take the avatar: " + reason(err.Error())
		}
		return why
	}
	if d.o.DrawAvatar == nil {
		return post("", avatar.ReasonNoModel)
	}
	svg, why := d.o.DrawAvatar(ctx, avatar.Request{
		DisplayName: cr.DisplayName, Palette: cr.Palette,
		ReportStyle: cr.Personality.ReportStyle, Focus: cr.Personality.Focus, Vocabulary: cr.Personality.Vocabulary,
	})
	if why != "" {
		return post("", sanitize.Line(why, 200))
	}
	// Admitted again here, whatever the drawer did: nothing but the guard's own
	// bytes is posted.
	kept, err := svgguard.Sanitize(svg)
	if err != nil {
		return post("", avatar.ReasonUnusable)
	}
	return post(string(kept), "")
}
