package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

func cacheApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("VULNETIX_WEB_URL", "")
	a := newPersistApp(t)
	t.Cleanup(a.keepAliveFor().Close)
	return a
}

func TestCacheStatusDefaultsOff(t *testing.T) {
	a := cacheApp(t)
	a.handleCommand("/cache")
	got := lastSystem(a)
	for _, want := range []string{"prompt cache: ttl 5m", "nothing sent yet", "keep-alive: off"} {
		if !strings.Contains(got, want) {
			t.Fatalf("/cache lacks %q:\n%s", want, got)
		}
	}
}

func TestCacheTTLAndKeepaliveWriteTheUserFile(t *testing.T) {
	a := cacheApp(t)
	a.handleCommand("/cache ttl 1h")
	if a.settings.PromptCacheTTL() != "1h" || a.eff.Origin["prompt_cache"] != config.SourceGlobal {
		t.Fatalf("ttl = %q from %q: %s", a.settings.PromptCacheTTL(), a.eff.Origin["prompt_cache"], lastSystem(a))
	}
	if !strings.Contains(lastSystem(a), "costs more") {
		t.Fatalf("the 1h write premium is not stated: %s", lastSystem(a))
	}

	a.handleCommand("/cache keepalive 3")
	if a.settings.PromptCacheKeepaliveMax() != 3 || a.keepAliveFor().Status().Max != 3 {
		t.Fatalf("keepalive = %d (status %+v)", a.settings.PromptCacheKeepaliveMax(), a.keepAliveFor().Status())
	}
	// 1h is still set, and its gap is the long default.
	if a.settings.PromptCacheKeepaliveEvery() != 3300*time.Second {
		t.Fatalf("gap = %v", a.settings.PromptCacheKeepaliveEvery())
	}
	a.handleCommand("/cache")
	if got := lastSystem(a); !strings.Contains(got, "0 of 3 ping(s)") || !strings.Contains(got, "waiting for the first request") {
		t.Fatalf("status:\n%s", got)
	}

	a.handleCommand("/cache keepalive off")
	if a.settings.PromptCacheKeepaliveMax() != 0 || a.keepAliveFor().Status().Max != 0 {
		t.Fatal("off did not turn the pings off")
	}
	a.handleCommand("/cache ttl 5m")
	if a.settings.PromptCacheTTL() != "5m" {
		t.Fatal("ttl 5m did not stick")
	}
}

func TestCacheCommandRefusesBadInput(t *testing.T) {
	a := cacheApp(t)
	for _, in := range []string{"/cache ttl 2h", "/cache ttl", "/cache keepalive", "/cache bogus"} {
		a.handleCommand(in)
		if !strings.Contains(lastSystem(a), "use /cache") {
			t.Errorf("%s: %q", in, lastSystem(a))
		}
	}
	a.handleCommand("/cache keepalive 999")
	if !strings.Contains(lastSystem(a), "0 to 100") || a.settings.PromptCacheKeepaliveMax() != 0 {
		t.Fatalf("an out-of-range count was accepted: %q", lastSystem(a))
	}
	a.handleCommand("/cache keepalive -2")
	if a.settings.PromptCacheKeepaliveMax() != 0 {
		t.Fatal("a negative count was accepted")
	}
}

func TestKeepAliveIsHeldWhereAPingMustNotRun(t *testing.T) {
	a := cacheApp(t)
	max := 2
	a.settings.PromptCache = &config.PromptCacheSettings{KeepaliveMax: &max}
	if a.keepAliveHeld() {
		t.Fatal("an idle chat session must be allowed to ping")
	}
	a.syncKeepAlive()
	if a.keepAliveFor().Status().Paused || a.keepAliveFor().Status().Max != 2 {
		t.Fatalf("status = %+v", a.keepAliveFor().Status())
	}

	a.planMode = true
	if !a.keepAliveHeld() {
		t.Fatal("plan mode must hold pings")
	}
	a.syncKeepAlive()
	if !a.keepAliveFor().Status().Paused {
		t.Fatal("the hold did not reach the keep-alive")
	}
	a.planMode = false

	_, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	if !a.keepAliveHeld() {
		t.Fatal("a running turn must hold pings")
	}
	a.cancel = nil
	cancel()

	a.preSend = true
	if !a.keepAliveHeld() {
		t.Fatal("a prompt being admitted must hold pings")
	}
	a.preSend = false

	a.kaBudgetRed = true
	if !a.keepAliveHeld() {
		t.Fatal("an exhausted budget must hold pings")
	}
	a.kaBudgetRed = false

	// With pings off nothing is configured and nothing is stored.
	a.settings.PromptCache = nil
	a.syncKeepAlive()
	if a.keepAliveFor().Status().Max != 0 {
		t.Fatal("pings off must reset the keep-alive")
	}
}

func TestCacheStatusLines(t *testing.T) {
	on := config.Settings{PromptCache: &config.PromptCacheSettings{TTL: "1h", KeepaliveMax: ip(4), KeepaliveEveryS: ip(600)}}
	cs := cacheStats{}
	cs.add(run.UsageEvent{Role: run.RoleAgent, Prompt: 1000, CacheRead: 900, CacheWrite: 50})

	cases := []struct {
		name string
		st   run.KeepAliveStatus
		want []string
	}{
		{"armed", run.KeepAliveStatus{Armed: true, Sent: 1, NextIn: 90 * time.Second}, []string{"ttl 1h", "last call 90% read", "1 of 4 ping(s)", "every 10m0s", "next in 1m30s"}},
		{"disabled", run.KeepAliveStatus{Disabled: true}, []string{"stopped for this session after a refused ping"}},
		{"paused", run.KeepAliveStatus{Paused: true, Armed: true}, []string{"held while a turn runs"}},
		{"spent", run.KeepAliveStatus{Armed: true, Sent: 4, Tokens: 4200, CacheRead: 4000}, []string{"pings so far: 4.2k tokens", "4k read from cache"}},
	}
	for _, c := range cases {
		got := cacheStatus(on, c.st, cs)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", c.name, w, got)
			}
		}
	}
	if got := cacheStatus(config.Settings{}, run.KeepAliveStatus{}, cacheStats{calls: 2, prompt: 10}); !strings.Contains(got, "reports no cache use") {
		t.Errorf("unreported cache: %s", got)
	}
}
