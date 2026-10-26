package rc

import (
	"context"
	"errors"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// Host requests. The daemon runs where nobody sits, so when it needs a person
// (a login that is about to expire) it asks the website, which tells the
// host's owner through the organisation's configured channel with an Approve
// and a Deny action (vdb-site docs/belai-host-attention.md). It never opens a
// browser: the old xdg-open raised error dialogs on a desktop and did nothing
// anywhere else.
//
// The server can only answer a host it can identify, so a request is made
// while the credential still works. A host whose credential is already
// rejected is not served; it shows as offline, as before.

const (
	// DefaultCredentialWarn is how long before a login's expiry the daemon asks.
	DefaultCredentialWarn = 30 * time.Minute
	// DefaultCredentialEvery is how often the daemon looks at the login's expiry.
	DefaultCredentialEvery = time.Minute
	// askAgain spaces the requests of one watch: a denied or lapsed request is
	// not repeated at once, because the person has already answered.
	askAgain = 20 * time.Minute
	// askUnsupported spaces them when the website cannot take a request at all.
	askUnsupported = time.Hour
	// redeemTries bounds the heartbeats a failing redeem is retried on.
	redeemTries = 5
	// redeemTimeout bounds one redeem plus storing the login.
	redeemTimeout = 45 * time.Second
)

// hostRequest is a request this daemon has made and not yet seen settled.
type hostRequest struct {
	kind       string
	deviceCode string
	expires    time.Time
	tries      int
}

// Request asks the owner, through the website, to sign this host in again
// (kind login) or to approve a step (kind approve). The reason comes from the
// server's closed list (sessionsync.Reason*). It returns once the website has
// the request; the outcome arrives on a later heartbeat (settleRequests).
func (d *Daemon) Request(ctx context.Context, kind, reason, subject string) error {
	grant, err := d.o.Client.RequestAttention(ctx, d.o.HostID, kind, reason, subject)
	switch {
	case errors.Is(err, sessionsync.ErrNotFound):
		d.logf("the website does not take host requests yet, so nobody was asked: %s", requestHint(kind))
		return err
	case errors.Is(err, sessionsync.ErrUnauthorized):
		d.logf("the website no longer accepts this host's login, so it cannot ask for a new one: %s", requestHint(kind))
		return err
	case err != nil:
		d.logf("could not ask for a %s: %v", kind, err)
		return err
	}
	expires := time.UnixMilli(grant.ExpiresAt)
	d.mu.Lock()
	if d.requests == nil {
		d.requests = map[string]*hostRequest{}
	}
	d.requests[grant.ID] = &hostRequest{kind: kind, deviceCode: grant.DeviceCode, expires: expires}
	d.mu.Unlock()
	d.logf("asked the owner to %s this host (request %s, open until %s); approve it in the web console",
		verbOf(kind), short(grant.ID), expires.Format("15:04"))
	return nil
}

func verbOf(kind string) string {
	if kind == sessionsync.RequestLogin {
		return "sign in"
	}
	return "approve"
}

// requestHint is what a person at this host can do instead.
func requestHint(kind string) string {
	if kind == sessionsync.RequestLogin {
		return "run `vulnetix auth login` on this host"
	}
	return "approve it from the web console"
}

// hasRequest reports whether a request of kind is still open.
func (d *Daemon) hasRequest(kind string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.requests {
		if r.kind == kind && time.Now().Before(r.expires) {
			return true
		}
	}
	return false
}

// settleRequests applies the outcomes a heartbeat reported. The same entry
// comes on several heartbeats, so it acts only on requests it still holds.
func (d *Daemon) settleRequests(ctx context.Context, list []sessionsync.HostRequest) {
	for _, r := range list {
		d.mu.Lock()
		p := d.requests[r.ID]
		d.mu.Unlock()
		if p == nil {
			continue
		}
		switch r.State {
		case sessionsync.RequestApproved:
			if d.finish(ctx, r.ID, p) {
				d.forget(r.ID)
			}
		case sessionsync.RequestDenied, sessionsync.RequestExpired, sessionsync.RequestCancelled:
			d.logf("the request to %s this host was %s (%s)", verbOf(p.kind), r.State, short(r.ID))
			d.forget(r.ID)
		}
	}
	d.mu.Lock()
	for id, p := range d.requests {
		if time.Now().After(p.expires.Add(time.Minute)) {
			delete(d.requests, id)
		}
	}
	d.mu.Unlock()
}

func (d *Daemon) forget(id string) {
	d.mu.Lock()
	delete(d.requests, id)
	d.mu.Unlock()
}

// finish carries out an approved request. It reports whether the request is
// done with: false keeps it for the next heartbeat (a login that could not be
// stored yet).
func (d *Daemon) finish(ctx context.Context, id string, p *hostRequest) bool {
	if p.kind != sessionsync.RequestLogin {
		d.logf("the request to %s this host was approved (%s)", verbOf(p.kind), short(id))
		return true
	}
	if d.o.RedeemLogin == nil || p.deviceCode == "" {
		d.logf("sign-in approved (%s), but this host cannot store it here: %s", short(id), requestHint(p.kind))
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, redeemTimeout)
	defer cancel()
	err := d.o.RedeemLogin(rctx, p.deviceCode)
	switch {
	case err == nil:
		d.logf("signed in again as approved (%s); the new login is read within five minutes", short(id))
		return true
	case errors.Is(err, vulnetixcli.ErrDeviceExpired), errors.Is(err, vulnetixcli.ErrDeviceDenied):
		d.logf("the approved sign-in could not be used (%v): %s", err, requestHint(p.kind))
		return true
	}
	p.tries++
	if p.tries >= redeemTries {
		d.logf("the approved sign-in could not be stored after %d tries (%v): %s", p.tries, err, requestHint(p.kind))
		return true
	}
	d.logf("the approved sign-in is not stored yet (%v); trying again", err)
	return false
}

// watchCredential asks for a fresh login when the current one is close to
// expiring. Only a credential with an expiry (a Bearer token login) can; an
// API key never expires, so for one this does nothing.
func (d *Daemon) watchCredential(ctx context.Context) {
	if d.o.CredentialExpiry == nil {
		return
	}
	t := time.NewTicker(d.o.CredentialEvery)
	defer t.Stop()
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		exp, ok := d.o.CredentialExpiry()
		if !ok || time.Until(exp) > d.o.CredentialWarn || time.Now().Before(next) || d.hasRequest(sessionsync.RequestLogin) {
			continue
		}
		err := d.Request(ctx, sessionsync.RequestLogin, sessionsync.ReasonCredentialExpiring, "")
		switch {
		case errors.Is(err, sessionsync.ErrNotFound), errors.Is(err, sessionsync.ErrUnauthorized):
			next = time.Now().Add(askUnsupported)
		default:
			next = time.Now().Add(askAgain)
		}
	}
}
