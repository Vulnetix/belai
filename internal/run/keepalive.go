package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/wire"
)

// RoleKeepalive is the usage role of a cache keep-alive ping. A ping is a
// model call the user pays for, so it reaches the budget ledger and the
// -usage-json summary under its own role and is left out of the agent-only
// figures (cache share, reply cost, request composition).
const RoleKeepalive = "keepalive"

// keepAlivePoll is how often the loop looks at the clock. A ping is due many
// seconds apart, so a coarse poll costs nothing.
const keepAlivePoll = 5 * time.Second

// keepAliveBusyMax bounds how long a request is treated as in flight. A call
// that failed in a way no path reported would otherwise block pings forever.
const keepAliveBusyMax = 15 * time.Minute

// noteKeepAlive hands a main-turn Anthropic request to the keep-alive. The
// factory is the one the turn sends through, so a ping rebuilds the same
// request, credential and route.
func noteKeepAlive(ctx context.Context, cfg Config, d dialect, system string, turns []Turn, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef, factory func(context.Context) (*http.Request, error)) {
	if cfg.KeepAlive == nil || d.kind != kindAnthropicMessages || !d.cache {
		return
	}
	cfg.KeepAlive.remember(ctx, cfg, system, turns, toolDefTokens(openAITools, anthropicTools), factory)
}

type pingKey struct{}

// isPing reports whether ctx belongs to a keep-alive ping.
func isPing(ctx context.Context) bool {
	v, _ := ctx.Value(pingKey{}).(bool)
	return v
}

// KeepAlive refreshes the provider's cache of the last main-turn request while
// the session is idle (docs/prompt-cache.md). It is the harness's own timer: a
// ping replays the sealed request the model last saw, through the request
// factory a normal turn uses (same provider, credential, netguard and firewall
// path, no second sender), as one non-streaming call whose reply is discarded.
// The reply never enters the conversation, a transcript or a classifier, so
// there is no sanitiser sink and no classifier kind for it. No tool runs and no
// hook fires, because a ping is not a turn.
//
// It stops on any non-2xx answer, at the configured count, and while its owner
// holds it paused (plan mode, an open ask, a running turn, an exhausted
// budget). Every error disables pings only.
type KeepAlive struct {
	mu     sync.Mutex
	client *http.Client
	now    func() time.Time

	max    int
	every  time.Duration
	paused bool

	// disabled is set by a failed ping and stays set for the session.
	disabled bool
	sent     int
	tokens   int
	read     int

	// The remembered request. Held in memory only and dropped by Reset.
	cfg     Config
	system  string
	turns   []Turn
	toolDef int
	factory func(context.Context) (*http.Request, error)
	vals    context.Context

	busy      bool
	busyStart time.Time
	last      time.Time
	pinging   bool

	stop chan struct{}
	// send is the test seam for the one network call.
	send func(ctx context.Context) (Assistant, int, error)
}

// KeepAliveStatus is what /cache shows: numbers and state, no request text.
type KeepAliveStatus struct {
	Max      int
	Sent     int
	Every    time.Duration
	Disabled bool
	Armed    bool
	Paused   bool
	// NextIn is the time to the next ping, zero when none is due or armed.
	NextIn time.Duration
	// Tokens is the total the pings spent, CacheRead the share of it the
	// provider served from cache.
	Tokens    int
	CacheRead int
}

// NewKeepAlive returns an idle keep-alive that sends through client (the
// session's own, so the transport, proxy and netguard are the turn's).
func NewKeepAlive(client *http.Client) *KeepAlive {
	return &KeepAlive{client: client, now: time.Now}
}

// Configure sets the ping count and the idle gap. A count of zero turns pings
// off and forgets the remembered request; a positive count starts the loop.
func (k *KeepAlive) Configure(max int, every time.Duration) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.max, k.every = max, every
	if max <= 0 {
		k.forgetLocked()
		k.stopLocked()
		return
	}
	if k.stop == nil {
		k.stop = make(chan struct{})
		go k.loop(k.stop)
	}
}

// SetPaused tells the keep-alive whether its owner is in a state that must not
// ping: plan mode, a turn running, an open permission or decision prompt, or an
// exhausted budget.
func (k *KeepAlive) SetPaused(p bool) {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.paused = p
	k.mu.Unlock()
}

// Rearm clears the disabled state a failed ping left. It is the user's answer
// (/cache keepalive N), never automatic.
func (k *KeepAlive) Rearm() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.disabled = false
	k.sent = 0
	k.mu.Unlock()
}

// Reset drops the remembered request, for a session rebuilt under another
// model, mode or tool set. The disabled state and the totals stay.
func (k *KeepAlive) Reset() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.forgetLocked()
	k.mu.Unlock()
}

// Close stops the loop and drops the remembered request. Safe to call twice.
func (k *KeepAlive) Close() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.forgetLocked()
	k.stopLocked()
	k.mu.Unlock()
}

func (k *KeepAlive) forgetLocked() {
	k.cfg, k.system, k.turns, k.factory, k.vals = Config{}, "", nil, nil, nil
	k.busy = false
}

func (k *KeepAlive) stopLocked() {
	if k.stop != nil {
		close(k.stop)
		k.stop = nil
	}
}

// Status reports the current state.
func (k *KeepAlive) Status() KeepAliveStatus {
	if k == nil {
		return KeepAliveStatus{}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	st := KeepAliveStatus{
		Max: k.max, Sent: k.sent, Every: k.every, Disabled: k.disabled,
		Armed: k.factory != nil && k.max > 0 && !k.disabled, Paused: k.paused,
		Tokens: k.tokens, CacheRead: k.read,
	}
	if st.Armed && !k.last.IsZero() && k.sent < k.max {
		if d := k.every - k.now().Sub(k.last); d > 0 {
			st.NextIn = d
		}
	}
	return st
}

// remember keeps the request a main turn just built so an idle gap can replay
// it, and starts the quiet-stretch clock. Only a main-turn call is kept: a
// classifier or role-manager call carries another prefix.
func (k *KeepAlive) remember(ctx context.Context, cfg Config, system string, turns []Turn, toolDefs int, factory func(context.Context) (*http.Request, error)) {
	if k == nil || usageRole(ctx) != RoleAgent {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.max <= 0 {
		return
	}
	cfg.KeepAlive = nil
	k.cfg, k.system, k.turns, k.toolDef, k.factory = cfg, system, turns, toolDefs, factory
	k.vals = context.WithoutCancel(ctx)
	k.busy, k.busyStart = true, k.now()
	k.last = k.busyStart
	k.sent = 0
}

// finish marks the request in flight as done and restarts the idle clock.
func (k *KeepAlive) finish() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.busy = false
	k.last = k.now()
	k.mu.Unlock()
}

func (k *KeepAlive) loop(stop <-chan struct{}) {
	t := time.NewTicker(keepAlivePoll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			k.Tick()
		}
	}
}

// Tick sends one ping when one is due and reports whether it did. The loop
// calls it; a test calls it with its own clock.
func (k *KeepAlive) Tick() bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	now := k.now()
	if k.max <= 0 || k.disabled || k.paused || k.factory == nil || k.sent >= k.max || k.pinging {
		k.mu.Unlock()
		return false
	}
	if k.busy && now.Sub(k.busyStart) < keepAliveBusyMax {
		k.mu.Unlock()
		return false
	}
	if now.Sub(k.last) < k.every {
		k.mu.Unlock()
		return false
	}
	k.pinging = true
	vals := k.vals
	k.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithValue(vals, pingKey{}, true), 2*time.Minute)
	defer cancel()
	send := k.send
	if send == nil {
		send = k.roundTrip
	}
	a, status, err := send(ctx)

	k.mu.Lock()
	k.pinging = false
	if err != nil || status < 200 || status >= 300 {
		// No retry: a refused or rate-limited ping ends the pings for the
		// session, and the request body is dropped with them.
		k.disabled = true
		k.forgetLocked()
		k.mu.Unlock()
		return true
	}
	k.sent++
	k.last = k.now()
	if a.Usage != nil {
		k.tokens += a.Usage.PromptTokens + a.Usage.CompletionTokens
		k.read += a.Usage.CacheReadTokens
	}
	k.mu.Unlock()
	return true
}

// roundTrip builds the remembered request as a ping, sends it once and books
// its usage. The reply text is dropped here: only the Assistant's usage leaves.
func (k *KeepAlive) roundTrip(ctx context.Context) (Assistant, int, error) {
	k.mu.Lock()
	cfg, system, turns, toolDefs, factory := k.cfg, k.system, k.turns, k.toolDef, k.factory
	client := k.client
	k.mu.Unlock()
	if factory == nil {
		return Assistant{}, 0, context.Canceled
	}
	req, err := factory(ctx)
	if err != nil {
		return Assistant{}, 0, err
	}
	redact := func(s string) string { return strings.ReplaceAll(s, cfg.APIKey, "<redacted>") }
	body, status, limits, err := roundTripLimits(ctx, client, req, cfg, redact)
	if err != nil {
		return Assistant{}, status, err
	}
	a, err := parseAnthropic(body, status, redact)
	if err != nil {
		return Assistant{}, status, err
	}
	reply := Assistant{Usage: a.Usage}
	reportUsage(withUsageRole(ctx, RoleKeepalive), cfg, system, turns, reply, toolDefs, limits)
	return reply, status, nil
}

// pingRequest shapes a rebuilt request as a ping: one blocking call with the
// smallest completion that keeps the request valid. Everything else, the
// system blocks, the tools, the messages and the thinking fields, stays as the
// turn sent it, since a change to any of them would be a different prefix.
func pingRequest(req *wire.AnthropicMessagesRequest) {
	req.Stream = false
	req.MaxTokens = 1
	if req.Thinking != nil && req.Thinking.BudgetTokens > 0 {
		// The API rejects a thinking budget that is not below max_tokens.
		req.MaxTokens = req.Thinking.BudgetTokens + 1
	}
}

// PrefixWatch notices a cached prefix that changed between two calls of one
// session: the stable system block or the tool definitions, with the tool set
// the same. A different tool set is an expected change and only moves the
// baseline. The digests are hashes, so no prompt text is held.
type PrefixWatch struct {
	mu     sync.Mutex
	set    bool
	names  string
	tools  string
	system string
}

// Prefix parts a watch can report.
const (
	PrefixSystem = "system"
	PrefixTools  = "tools"
)

// Observe records a call's prefix and returns the part that changed since the
// last call ("" for none, for the first call and for a different tool set).
func (w *PrefixWatch) Observe(system string, anthropicTools []wire.AnthropicToolDef, openAITools []wire.OpenAITool) string {
	names, tools := toolDigests(anthropicTools, openAITools)
	sys := systemDigest(system)
	w.mu.Lock()
	defer w.mu.Unlock()
	set, pn, pt, ps := w.set, w.names, w.tools, w.system
	w.set, w.names, w.tools, w.system = true, names, tools, sys
	switch {
	case !set, pn != names:
		return ""
	case pt != tools:
		return PrefixTools
	case ps != sys:
		return PrefixSystem
	}
	return ""
}

// systemDigest hashes the stable (first sealed) block with its nonce removed,
// as promptCacheKey does, so a fresh seal of the same text is not a change.
func systemDigest(system string) string {
	parts := splitSealedBlocks(system)
	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(sealNonceAttr.ReplaceAllString(parts[0], "")))
	return hex.EncodeToString(sum[:])
}

// toolDigests returns a hash of the sorted tool names and a hash of the whole
// definitions. Neither carries a name or a description out.
func toolDigests(anthropicTools []wire.AnthropicToolDef, openAITools []wire.OpenAITool) (names, defs string) {
	var list []string
	var v any
	switch {
	case len(anthropicTools) > 0:
		for _, t := range anthropicTools {
			list = append(list, t.Name)
		}
		v = anthropicTools
	case len(openAITools) > 0:
		for _, t := range openAITools {
			list = append(list, t.Function.Name)
		}
		v = openAITools
	default:
		return "", ""
	}
	sort.Strings(list)
	n := sha256.Sum256([]byte(strings.Join(list, "\x00")))
	b, err := json.Marshal(v)
	if err != nil {
		return hex.EncodeToString(n[:]), ""
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(n[:]), hex.EncodeToString(d[:])
}
