package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

// keepAliveFor returns the App's prompt-cache keep-alive, creating it on first
// use. It sends through the session's own HTTP client, so a ping takes the
// route and the checks a turn takes.
func (a *App) keepAliveFor() *run.KeepAlive {
	if a.keepAlive == nil {
		a.keepAlive = run.NewKeepAlive(a.client)
	}
	return a.keepAlive
}

// syncKeepAlive applies the settings to the keep-alive and tells it whether a
// ping is allowed right now. Update runs it after every message, so a state
// change (a plan-mode switch, an ask opening) holds pings before the next poll.
func (a *App) syncKeepAlive() {
	ka := a.keepAliveFor()
	n := a.settings.PromptCacheKeepaliveMax()
	ka.Configure(n, a.settings.PromptCacheKeepaliveEvery())
	if n == 0 {
		return
	}
	ka.SetPaused(a.keepAliveHeld())
}

// keepAliveHeld reports whether a ping must wait: plan mode, a turn running or
// being admitted, any open permission, clarification or plan-review prompt, an
// exhausted budget, or a local provider (a loopback server has no cache bill).
func (a *App) keepAliveHeld() bool {
	return a.planMode || a.working() || a.preSend ||
		a.permAskOpen() || a.clarifyOpen() || a.planReviewOpen() ||
		a.kaBudgetRed || a.isLocalProvider(a.cfg.Provider)
}

// noteKeepAliveBudget re-reads the selected model's budgets after a call, so a
// budget that has just run out holds the pings. Only the ledger's counts are
// read, and only while pings are on.
func (a *App) noteKeepAliveBudget() {
	if a.settings.PromptCacheKeepaliveMax() == 0 || a.budgets == nil {
		a.kaBudgetRed = false
		return
	}
	a.kaBudgetRed = budget.Worst(a.budgets.Gauges(a.settings.BudgetsFor(a.cfg.Provider, a.cfg.Model))) == budget.Red
}

// cacheCommand is /cache. With no argument, or status, it reports the prompt
// cache; ttl 5m|1h and keepalive N|off write the user's settings file, so a
// repository can neither choose nor override them (docs/prompt-cache.md).
func (a *App) cacheCommand(arg string) tea.Cmd {
	f := strings.Fields(strings.ToLower(arg))
	if len(f) == 0 || f[0] == "status" {
		a.addSystem(cacheStatus(a.settings, a.keepAliveFor().Status(), a.cacheStats))
		return nil
	}
	switch {
	case f[0] == "ttl" && len(f) == 2 && (f[1] == config.PromptCacheTTL5m || f[1] == config.PromptCacheTTL1h):
		ttl := f[1]
		if err := a.mutateGlobalSetting(func(s *config.Settings) {
			if s.PromptCache == nil {
				s.PromptCache = &config.PromptCacheSettings{}
			}
			s.PromptCache.TTL = ttl
		}); err != nil {
			a.addSystem("cache: could not save: " + err.Error())
			return nil
		}
		a.invalidateAgentSession()
		line := "cache: ttl " + ttl + " from the next request; the prefix is written once at the new lifetime"
		if ttl == config.PromptCacheTTL1h {
			line += " (a one hour write costs more than a five minute one)"
		}
		a.addSystem(line)
	case f[0] == "keepalive" && len(f) == 2:
		n := 0
		if f[1] != "off" {
			v, err := strconv.Atoi(f[1])
			if err != nil || v < 0 || v > config.MaxPromptCacheKeepalive {
				a.addSystem(fmt.Sprintf("cache: keepalive takes off or a count from 0 to %d", config.MaxPromptCacheKeepalive))
				return nil
			}
			n = v
		}
		if err := a.mutateGlobalSetting(func(s *config.Settings) {
			if s.PromptCache == nil {
				s.PromptCache = &config.PromptCacheSettings{}
			}
			s.PromptCache.KeepaliveMax = &n
		}); err != nil {
			a.addSystem("cache: could not save: " + err.Error())
			return nil
		}
		a.keepAliveFor().Rearm()
		a.syncKeepAlive()
		if n == 0 {
			a.addSystem("cache: keep-alive off")
		} else {
			a.addSystem(fmt.Sprintf("cache: up to %d ping(s), %s apart, while the session is idle; each ping is a paid call", n, a.settings.PromptCacheKeepaliveEvery()))
		}
	default:
		a.addSystem("cache: use /cache, /cache ttl 5m|1h or /cache keepalive N|off")
	}
	return nil
}

// cacheStatus composes the /cache report from settings, the keep-alive's state
// and the session's cache counts: numbers and harness words only.
func cacheStatus(s config.Settings, st run.KeepAliveStatus, cs cacheStats) string {
	b := &strings.Builder{}
	b.WriteString("prompt cache: ttl " + s.PromptCacheTTL())
	switch {
	case cs.calls == 0:
		b.WriteString("; nothing sent yet")
	case !cs.reported():
		b.WriteString("; the provider reports no cache use")
	default:
		fmt.Fprintf(b, "; last call %d%% read, session %d%% read over %d calls, %s tokens written",
			cachePct(cs.last.CacheRead, cs.last.Prompt), cachePct(cs.read, cs.prompt), cs.calls, humanTokens(int64(cs.write)))
	}
	n := s.PromptCacheKeepaliveMax()
	if n == 0 {
		b.WriteString("\nkeep-alive: off (/cache keepalive N turns it on)")
		return b.String()
	}
	fmt.Fprintf(b, "\nkeep-alive: %d of %d ping(s) sent, one every %s of idle", st.Sent, n, s.PromptCacheKeepaliveEvery())
	switch {
	case st.Disabled:
		b.WriteString("; stopped for this session after a refused ping (/cache keepalive N starts it again)")
	case st.Paused:
		b.WriteString("; held while a turn runs, a prompt is open, plan mode is on or a budget is spent")
	case !st.Armed:
		b.WriteString("; waiting for the first request")
	case st.NextIn > 0:
		b.WriteString("; next in " + st.NextIn.Round(time.Second).String())
	}
	if st.Tokens > 0 {
		fmt.Fprintf(b, "\npings so far: %s tokens, %s read from cache", humanTokens(int64(st.Tokens)), humanTokens(int64(st.CacheRead)))
	}
	return b.String()
}
