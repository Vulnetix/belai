package run

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/nonce"
)

// FirewallSource is implemented by a CredentialSource that can route a
// provider through the active AI Firewall.
type FirewallSource interface {
	Firewall(provider string) (firewall.Route, bool)
}

// FirewallObserver receives each firewall event (a block, refusal,
// redaction, flag or strip) from any model call. It is called on the
// goroutine that made the call, so it must not block. A clean pass is not an
// event.
type FirewallObserver func(firewall.Verdict)

// FirewallPassObserver receives one call per routed response that carried no
// event, for footer counters. It must not block.
type FirewallPassObserver func(instance string)

var (
	firewallObserver atomic.Pointer[FirewallObserver]
	firewallPass     atomic.Pointer[FirewallPassObserver]
	firewallMu       sync.Mutex
)

// SetFirewallObserver registers the process-wide firewall observer and
// returns a cancel that detaches it; it follows SetUsageObserver.
func SetFirewallObserver(fn FirewallObserver, pass FirewallPassObserver) (cancel func()) {
	firewallMu.Lock()
	defer firewallMu.Unlock()
	if fn == nil {
		firewallObserver.Store(nil)
		firewallPass.Store(nil)
		return func() {}
	}
	firewallObserver.Store(&fn)
	if pass != nil {
		firewallPass.Store(&pass)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if firewallObserver.CompareAndSwap(&fn, nil) {
				firewallPass.Store(nil)
			}
		})
	}
}

// inspectFirewall reads one response for firewall events: the route's own
// adapter, plus the provider-native guardrails of the provider called. body
// is nil for a stream.
func inspectFirewall(cfg Config, resp *http.Response, body []byte) {
	obs := firewallObserver.Load()
	if obs == nil || resp == nil {
		return
	}
	verdicts := firewall.Inspect(cfg.Firewall, firewall.InspectInput{
		Provider: cfg.Provider, Model: cfg.Model,
		Status: resp.StatusCode, Header: resp.Header, Body: body,
	})
	for _, v := range verdicts {
		(*obs)(v)
	}
	if len(verdicts) == 0 && cfg.Firewall != nil && resp.StatusCode < 300 {
		if p := firewallPass.Load(); p != nil {
			(*p)(cfg.Firewall.Instance)
		}
	}
}

// errFirewallRedirect refuses a redirect on a routed call: the request
// carries the firewall's key, which is for its own URL only.
var errFirewallRedirect = errors.New("the firewall redirected the request; refusing to follow")

// firewallClient returns client unchanged for a direct call, and a copy that
// never follows a redirect for a routed one.
func firewallClient(client *http.Client, cfg Config) *http.Client {
	if cfg.Firewall == nil {
		return client
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errFirewallRedirect }
	return &c
}

type noncePoolKey struct{}

// withNoncePool records the call's nonce pool so the request factory can
// tell the firewall whether every sealed block carries one of its nonces.
func withNoncePool(ctx context.Context, pool *nonce.Pool) context.Context {
	if pool == nil {
		return ctx
	}
	return context.WithValue(ctx, noncePoolKey{}, pool)
}

// applyFirewall sets a routed request's firewall headers. The nonce-mode
// header opts into the gateway stripping blocks it cannot verify; it is sent
// only while the call's pool is remote, i.e. every nonce came from the
// gateway this request goes to.
func applyFirewall(ctx context.Context, cfg Config, h http.Header) {
	if cfg.Firewall == nil {
		return
	}
	cfg.Firewall.Apply(h)
	if cfg.Firewall.AdapterID != "vulnetix" {
		return
	}
	if pool, ok := ctx.Value(noncePoolKey{}).(*nonce.Pool); ok && pool.RemoteFor(cfg.BaseURL) {
		h.Set(firewall.HeaderNonceMode, "enforce")
	}
}
