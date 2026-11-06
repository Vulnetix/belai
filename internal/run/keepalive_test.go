package run

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/wire"
)

func TestCacheTTLReachesEveryBreakpoint(t *testing.T) {
	tools := []wire.AnthropicToolDef{{Name: "Read", InputSchema: map[string]any{}}, {Name: "Grep", InputSchema: map[string]any{}}}
	turns := []Turn{{Role: "user", Content: "hi"}}

	plain := anthropicCfg("claude-opus-4-5", "")
	b, _ := json.Marshal(requestBody(t, plain, turns, true, tools))
	if strings.Contains(string(b), `"ttl"`) || !strings.Contains(string(b), "cache_control") {
		t.Fatalf("the default request must keep the plain marker: %s", b)
	}

	long := plain
	long.CacheTTL = "1h"
	body := requestBody(t, long, turns, true, tools)
	b, _ = json.Marshal(body)
	// system, last tool and last message: three markers here, each with the ttl.
	if n, m := strings.Count(string(b), `"ttl":"1h"`), strings.Count(string(b), "cache_control"); n != m || n != 3 {
		t.Fatalf("%d of %d breakpoints carry the ttl: %s", n, m, b)
	}
	if tools[1].CacheControl != nil {
		t.Fatal("the shared tool slice must not be marked in place")
	}

	// A route that sends no breakpoint sends no ttl either.
	gw := Config{Provider: "cloudflare-ai-gateway", BaseURL: "https://gw.example/compat", APIKey: "k", Model: "claude-sonnet-4-5", CacheTTL: "1h"}
	b, _ = json.Marshal(requestBody(t, gw, turns, true, nil))
	if strings.Contains(string(b), "ttl") || strings.Contains(string(b), "cache_control") {
		t.Fatalf("gateway route must not send a ttl: %s", b)
	}
}

func TestPingRequestKeepsThePrefixAndShrinksTheReply(t *testing.T) {
	cfg := anthropicCfg("claude-haiku-4-5", "high")
	cfg.CacheTTL = "1h"
	turns := []Turn{{Role: "user", Content: "hi"}}
	tools := []wire.AnthropicToolDef{{Name: "Read", InputSchema: map[string]any{}}}

	factory, _, err := newRequestFactory(cfg, "sys", turns, true, nil, tools)
	if err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context) map[string]any {
		req, err := factory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(req.Body)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	turn := read(context.Background())
	ping := read(context.WithValue(context.Background(), pingKey{}, true))

	if turn["stream"] != true || ping["stream"] == true {
		t.Fatalf("stream: turn %v ping %v", turn["stream"], ping["stream"])
	}
	thinking, _ := ping["thinking"].(map[string]any)
	budget, _ := thinking["budget_tokens"].(float64)
	if budget == 0 || ping["max_tokens"] != budget+1 {
		t.Fatalf("a thinking budget must stay below max_tokens: %v", ping)
	}
	for _, k := range []string{"system", "tools", "messages", "thinking", "model"} {
		a, _ := json.Marshal(turn[k])
		b, _ := json.Marshal(ping[k])
		if string(a) != string(b) {
			t.Errorf("%s differs, so the ping would not hit the cached prefix:\n%s\n%s", k, a, b)
		}
	}

	// With no thinking the cap is one token.
	plain := anthropicCfg("claude-haiku-4-5", "")
	factory, _, _ = newRequestFactory(plain, "sys", turns, true, nil, tools)
	if m := read(context.WithValue(context.Background(), pingKey{}, true)); m["max_tokens"] != float64(1) {
		t.Fatalf("max_tokens = %v", m["max_tokens"])
	}
}

// kaServer answers every request like the messages API and records the bodies.
type kaServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func newKAServer(t *testing.T) *kaServer {
	t.Helper()
	k := &kaServer{status: 200}
	k.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		k.mu.Lock()
		k.bodies = append(k.bodies, m)
		status := k.status
		k.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"SECRET-REPLY"}],"stop_reason":"max_tokens","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":900}}`))
	}))
	t.Cleanup(k.srv.Close)
	return k
}

func (k *kaServer) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.bodies)
}

// kaFixture sends one main turn through a keep-alive on a fake clock.
type kaFixture struct {
	t     *testing.T
	ka    *KeepAlive
	srv   *kaServer
	cfg   Config
	clock time.Time
	pings []UsageEvent
}

func newKAFixture(t *testing.T, max int) *kaFixture {
	t.Helper()
	srv := newKAServer(t)
	f := &kaFixture{t: t, srv: srv, clock: time.Unix(1_700_000_000, 0)}
	f.ka = NewKeepAlive(srv.srv.Client())
	f.ka.now = func() time.Time { return f.clock }
	f.ka.Configure(max, 270*time.Second)
	t.Cleanup(f.ka.Close)
	f.cfg = Config{Provider: "anthropic", BaseURL: srv.srv.URL, APIKey: "sk-test", Model: "claude-sonnet-5", KeepAlive: f.ka}
	var mu sync.Mutex
	cancel := SetUsageObserver(func(ev UsageEvent) {
		mu.Lock()
		defer mu.Unlock()
		if ev.Role == RoleKeepalive {
			f.pings = append(f.pings, ev)
		}
	})
	t.Cleanup(cancel)
	return f
}

func (f *kaFixture) turn() {
	f.t.Helper()
	_, err := SendTurnsWithTools(context.Background(), f.cfg, "sys", []Turn{{Role: "user", Content: "hi"}}, f.srv.srv.Client(), nil, nil, nil)
	if err != nil {
		f.t.Fatalf("turn: %v", err)
	}
}

func TestKeepAlivePingsWhenIdleAndBooksTheTokens(t *testing.T) {
	f := newKAFixture(t, 2)
	f.turn()
	if f.srv.count() != 1 {
		t.Fatalf("one turn = %d requests", f.srv.count())
	}
	if f.ka.Tick() {
		t.Fatal("pinged before the gap")
	}
	f.clock = f.clock.Add(269 * time.Second)
	if f.ka.Tick() {
		t.Fatal("pinged 1 s early")
	}
	f.clock = f.clock.Add(2 * time.Second)
	if !f.ka.Tick() || f.srv.count() != 2 {
		t.Fatalf("no ping at the gap (%d requests)", f.srv.count())
	}
	turn, ping := f.srv.bodies[0], f.srv.bodies[1]
	for _, k := range []string{"system", "tools", "messages"} {
		a, _ := json.Marshal(turn[k])
		b, _ := json.Marshal(ping[k])
		if string(a) != string(b) {
			t.Errorf("%s differs between the turn and the ping", k)
		}
	}
	if ping["max_tokens"] != float64(1) {
		t.Fatalf("ping max_tokens = %v", ping["max_tokens"])
	}
	if len(f.pings) != 1 || f.pings[0].Tokens != 911 || f.pings[0].CacheRead != 900 {
		t.Fatalf("ping usage = %+v", f.pings)
	}
	st := f.ka.Status()
	if st.Sent != 1 || st.Tokens != 911 || st.CacheRead != 900 || st.Disabled {
		t.Fatalf("status = %+v", st)
	}

	// The second ping lands one gap after the first, then the count is spent.
	f.clock = f.clock.Add(270 * time.Second)
	if !f.ka.Tick() {
		t.Fatal("second ping missing")
	}
	f.clock = f.clock.Add(time.Hour)
	if f.ka.Tick() || f.srv.count() != 3 {
		t.Fatalf("pinged past keepalive_max (%d requests)", f.srv.count())
	}

	// A real turn starts a new quiet stretch with a fresh count.
	f.turn()
	f.clock = f.clock.Add(271 * time.Second)
	if !f.ka.Tick() {
		t.Fatal("no ping after a new turn")
	}
}

func TestKeepAliveDiscardsTheReply(t *testing.T) {
	f := newKAFixture(t, 1)
	f.turn()
	f.clock = f.clock.Add(time.Hour)
	if !f.ka.Tick() {
		t.Fatal("no ping")
	}
	// The only thing a ping leaves is a usage event of numbers.
	for _, ev := range f.pings {
		if strings.Contains(ev.Model+ev.Provider+ev.Role, "SECRET") {
			t.Fatal("reply text reached a usage event")
		}
	}
	if st := f.ka.Status(); strings.Contains(strings.Join([]string{f.ka.system}, ""), "SECRET-REPLY") || st.Sent != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestKeepAliveStopConditions(t *testing.T) {
	t.Run("paused", func(t *testing.T) {
		f := newKAFixture(t, 3)
		f.turn()
		f.clock = f.clock.Add(time.Hour)
		f.ka.SetPaused(true)
		if f.ka.Tick() || f.srv.count() != 1 {
			t.Fatal("pinged while paused")
		}
		f.ka.SetPaused(false)
		if !f.ka.Tick() {
			t.Fatal("did not resume")
		}
	})
	t.Run("a request in flight", func(t *testing.T) {
		f := newKAFixture(t, 3)
		f.turn()
		f.clock = f.clock.Add(time.Hour)
		f.ka.mu.Lock()
		f.ka.busy, f.ka.busyStart = true, f.clock
		f.ka.mu.Unlock()
		if f.ka.Tick() {
			t.Fatal("pinged during a request")
		}
		f.clock = f.clock.Add(keepAliveBusyMax)
		if !f.ka.Tick() {
			t.Fatal("a request that never finished must not block pings for ever")
		}
	})
	t.Run("a refused ping disables and drops the body", func(t *testing.T) {
		f := newKAFixture(t, 3)
		f.turn()
		f.srv.mu.Lock()
		f.srv.status = 429
		f.srv.mu.Unlock()
		f.clock = f.clock.Add(time.Hour)
		f.ka.Tick()
		st := f.ka.Status()
		if !st.Disabled || st.Armed || st.Sent != 0 {
			t.Fatalf("status = %+v", st)
		}
		before := f.srv.count()
		f.srv.mu.Lock()
		f.srv.status = 200
		f.srv.mu.Unlock()
		f.turn() // a real turn remembers a request again, but the session stays disabled
		f.clock = f.clock.Add(time.Hour)
		if f.ka.Tick() || f.srv.count() != before+1 {
			t.Fatal("pinged after a failure with no rearm")
		}
		f.ka.Rearm()
		if !f.ka.Tick() {
			t.Fatal("rearm did not allow pings")
		}
	})
	t.Run("reset and off forget the request", func(t *testing.T) {
		f := newKAFixture(t, 3)
		f.turn()
		f.ka.Reset()
		f.clock = f.clock.Add(time.Hour)
		if f.ka.Tick() || f.ka.Status().Armed {
			t.Fatal("pinged after a reset")
		}
		f.turn()
		f.ka.Configure(0, time.Minute)
		if f.ka.factory != nil || len(f.ka.turns) != 0 || f.ka.stop != nil {
			t.Fatal("turning pings off must drop the stored request")
		}
	})
	t.Run("off by default", func(t *testing.T) {
		f := newKAFixture(t, 0)
		f.turn()
		if f.ka.factory != nil {
			t.Fatal("a keep-alive that is off stored a request")
		}
	})
}

func TestKeepAliveOnlyRemembersAMainTurn(t *testing.T) {
	f := newKAFixture(t, 3)
	ctx := withUsageRole(context.Background(), RoleSecurity)
	if _, err := SendTurnsWithTools(ctx, f.cfg, "sys", []Turn{{Role: "user", Content: "classify"}}, f.srv.srv.Client(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.ka.factory != nil {
		t.Fatal("a classifier call was remembered as the prefix to keep warm")
	}
	// Nor does a non-Anthropic route.
	oa := Config{Provider: "openai", BaseURL: f.srv.srv.URL, APIKey: "k", Model: "gpt-5", KeepAlive: f.ka}
	_, _ = SendTurnsWithTools(context.Background(), oa, "sys", []Turn{{Role: "user", Content: "hi"}}, f.srv.srv.Client(), nil, nil, nil)
	if f.ka.factory != nil {
		t.Fatal("a provider with no cache_control was remembered")
	}
}

func TestPrefixWatch(t *testing.T) {
	sys := func(nonce, text string) string {
		return `<system nonce="` + nonce + `">` + text + "</system>\n" + `<system nonce="` + nonce + `">session</system>` + "\n"
	}
	a := []wire.AnthropicToolDef{{Name: "Read", Description: "d1"}, {Name: "Grep"}}
	var w PrefixWatch
	if got := w.Observe(sys("n1", "stable"), a, nil); got != "" {
		t.Fatalf("first call = %q", got)
	}
	// A fresh nonce on the same text is not a change.
	if got := w.Observe(sys("n2", "stable"), a, nil); got != "" {
		t.Fatalf("nonce only = %q", got)
	}
	// The session block is expected to move.
	if got := w.Observe(`<system nonce="n3">stable</system>`+"\n"+`<system nonce="n3">other session</system>`+"\n", a, nil); got != "" {
		t.Fatalf("session block = %q", got)
	}
	if got := w.Observe(sys("n4", "stable edited"), a, nil); got != PrefixSystem {
		t.Fatalf("stable block = %q", got)
	}
	b := []wire.AnthropicToolDef{{Name: "Read", Description: "d2"}, {Name: "Grep"}}
	if got := w.Observe(sys("n4", "stable edited"), b, nil); got != PrefixTools {
		t.Fatalf("tool description = %q", got)
	}
	// A different tool set (a mode switch, a deferred tool loaded) only moves the baseline.
	if got := w.Observe(sys("n4", "stable edited"), append(b, wire.AnthropicToolDef{Name: "Edit"}), nil); got != "" {
		t.Fatalf("tool set change = %q", got)
	}
	if got := w.Observe(sys("n4", "stable edited"), append(b, wire.AnthropicToolDef{Name: "Edit"}), nil); got != "" {
		t.Fatalf("steady = %q", got)
	}
}
