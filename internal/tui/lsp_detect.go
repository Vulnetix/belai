package tui

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/lsp"
)

// lspProbeMsg carries the result of one detection run.
type lspProbeMsg struct {
	found    map[string]bool
	fallback map[string]bool
	probedAt time.Time
}

// lspDetectState caches PATH probes for the language-servers view.
type lspDetectState struct {
	mu       sync.Mutex
	langs    []lsp.Language
	found    map[string]bool
	fallback map[string]bool
	probedAt time.Time
	inFlight bool
}

const (
	lspDetectTTL     = 60 * time.Second
	lspDetectTimeout = 3 * time.Second
)

// enterLSP starts or reuses a language-server detection pass.
func (a *App) enterLSP() tea.Cmd {
	a.lspDetect.mu.Lock()
	if a.lspDetect.inFlight {
		a.lspDetect.mu.Unlock()
		return nil
	}
	if a.lspDetect.langs == nil {
		a.lspDetect.langs = lsp.Languages()
		if a.lspDetect.found == nil {
			a.lspDetect.found = map[string]bool{}
		}
		if a.lspDetect.fallback == nil {
			a.lspDetect.fallback = map[string]bool{}
		}
	}
	now := time.Now()
	if now.Sub(a.lspDetect.probedAt) < lspDetectTTL {
		a.lspDetect.mu.Unlock()
		return nil
	}
	a.lspDetect.inFlight = true
	a.lspDetect.mu.Unlock()

	return func() tea.Msg {
		found, fallback := a.probeLSPCmd()
		return lspProbeMsg{found: found, fallback: fallback, probedAt: time.Now()}
	}
}

// probeLSPCmd runs PATH lookups and fallback checks. It must not touch *App
// after returning to avoid races with the Bubble Tea goroutine.
func (a *App) probeLSPCmd() (found, fallback map[string]bool) {
	found = map[string]bool{}
	fallback = map[string]bool{}
	if runtime.GOOS == "windows" {
		return
	}
	settings := a.settings
	ctx, cancel := context.WithTimeout(context.Background(), lspDetectTimeout)
	defer cancel()
	detected := lsp.DetectAll(ctx, map[string]string{})
	for _, lang := range lsp.Languages() {
		if _, ok := detected[lang.ID]; ok {
			found[lang.ID] = true
		}
		// Degrade open for display: show a row even with no server.
		if settings.LSPFallbackEnabled() && len(lang.Fallback) > 0 {
			fallback[lang.ID] = true
		}
		_ = ctx.Err()
	}
	return found, fallback
}

// handleLSPProbe updates the cache.
func (a *App) handleLSPProbe(msg lspProbeMsg) {
	a.lspDetect.mu.Lock()
	defer a.lspDetect.mu.Unlock()
	a.lspDetect.inFlight = false
	a.lspDetect.probedAt = msg.probedAt
	a.lspDetect.found = msg.found
	a.lspDetect.fallback = msg.fallback
}

// invalidateLSPDetect clears the probe cache so the next render re-detects.
func (a *App) invalidateLSPDetect() {
	a.lspDetect.mu.Lock()
	defer a.lspDetect.mu.Unlock()
	a.lspDetect.probedAt = time.Time{}
}

// detectedLanguageIDs returns a sorted list of detected IDs for stable rendering.
func (a *App) detectedLanguageIDs() []string {
	a.lspDetect.mu.Lock()
	defer a.lspDetect.mu.Unlock()
	out := make([]string, 0, len(a.lspDetect.found))
	for id := range a.lspDetect.found {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
