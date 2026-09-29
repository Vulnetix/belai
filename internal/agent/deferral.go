package agent

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/wire"
)

// Deferred tools (see internal/tools/toolsearch.go). The session's surfaces
// are unchanged — surfaceFull still decides what the current mode permits —
// and toolSurface only chooses which of those definitions ride on the
// request: the core tools, ToolSearch, and whatever the model has loaded.
// Every tool on the surface stays callable by name; deferral trims the
// request, not the capability.

// toolDeferral is a session's loaded set and its rendered surfaces.
type toolDeferral struct {
	mu     sync.Mutex
	loaded []string
	cache  map[string]renderedSurface
}

// renderedSurface is one advertised registry and its wire forms.
type renderedSurface struct {
	reg       *tools.Registry
	openAI    []wire.OpenAITool
	anthropic []wire.AnthropicToolDef
}

func (d *toolDeferral) loadedNames() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.loaded)
}

// toolSurface returns the registry and wire definitions the request
// advertises: surfaceFull, with deferred tools held back until loaded.
func (s *Session) toolSurface() (*tools.Registry, []wire.OpenAITool, []wire.AnthropicToolDef) {
	reg, oa, an := s.surfaceFull()
	if s.deferral == nil {
		return reg, oa, an
	}
	loaded := s.deferral.loadedNames()
	adv, deferred := tools.DeferSet(reg.Definitions(), loaded)
	names := make([]string, 0, len(adv))
	for _, d := range adv {
		// On a surface that never had anything to defer, ToolSearch is noise.
		// Once it has loaded something it stays, so the tool list keeps its
		// cached prefix and the earlier ToolSearch calls stay advertised.
		if len(deferred) == 0 && len(loaded) == 0 && len(s.skillCands) == 0 && d.Name == tools.ToolSearchName {
			continue
		}
		names = append(names, d.Name)
	}
	// The surface as it stands is reused only when nothing is held back and
	// nothing reordered (loaded tools are appended, not left in place).
	if slices.Equal(names, reg.Names()) {
		return reg, oa, an
	}
	key := strings.Join(names, "\x00")
	s.deferral.mu.Lock()
	defer s.deferral.mu.Unlock()
	if r, ok := s.deferral.cache[key]; ok {
		return r.reg, r.openAI, r.anthropic
	}
	sub := reg.Only(names...)
	soa, san := wireTools(sub)
	if s.deferral.cache == nil {
		s.deferral.cache = map[string]renderedSurface{}
	}
	s.deferral.cache[key] = renderedSurface{reg: sub, openAI: soa, anthropic: san}
	return sub, soa, san
}

// deferredNames lists the current surface's tools not loaded yet, for the
// sealed tools briefing.
func (s *Session) deferredNames() []string {
	defs := s.Deferred()
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}

// Deferred implements tools.ToolCatalog: the current surface's tools that are
// not advertised yet.
func (s *Session) Deferred() []tools.Definition {
	if s.deferral == nil {
		return nil
	}
	reg, _, _ := s.surfaceFull()
	_, deferred := tools.DeferSet(reg.Definitions(), s.deferral.loadedNames())
	return deferred
}

// Load implements tools.ToolCatalog. Only tools on the current surface can
// be loaded, so plan mode cannot load a writer.
func (s *Session) Load(names []string) []string {
	if s.deferral == nil {
		return nil
	}
	var out []string
	available := s.Deferred()
	s.deferral.mu.Lock()
	defer s.deferral.mu.Unlock()
	for _, n := range names {
		for _, d := range available {
			if strings.EqualFold(d.Name, n) && !slices.Contains(s.deferral.loaded, d.Name) {
				s.deferral.loaded = append(s.deferral.loaded, d.Name)
				out = append(out, d.Name)
			}
		}
	}
	return out
}

// deferCatalog lets the registry hold ToolSearch before the session that
// answers it exists.
type deferCatalog struct{ s *Session }

func (c *deferCatalog) Deferred() []tools.Definition {
	if c.s == nil {
		return nil
	}
	return c.s.Deferred()
}

// Skills and Rank forward to the session, so ToolSearch can search installed
// skills and refine a keyword search with the decision backend.
func (c *deferCatalog) Skills() []tools.Candidate {
	if c.s == nil {
		return nil
	}
	return c.s.Skills()
}

func (c *deferCatalog) Rank(ctx context.Context, query string, det, pool []tools.Candidate, limit int) ([]tools.Candidate, bool) {
	if c.s == nil {
		return nil, false
	}
	return c.s.Rank(ctx, query, det, pool, limit)
}

func (c *deferCatalog) Load(names []string) []string {
	if c.s == nil {
		return nil
	}
	return c.s.Load(names)
}
