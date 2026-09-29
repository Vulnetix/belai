package agent

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// Explore locate. Before explore subagents start, the files the goal is about
// are ranked (internal/locate: an eligibility-first inventory, a keyword
// order, and the decision backend reordering directories and then files), and
// each subagent is told where to begin. The ranking is paths and line numbers
// the harness computed; it holds no file text, so it needs no classification.
// The job narrows where subagents look and never approves anything: without a
// backend answer the keyword order stands, and with the job off nothing runs.
const (
	// locateInventoryTTL is how long a walk of the working directory is reused.
	locateInventoryTTL = 90 * time.Second
	// locateWalkBudget bounds one walk; a partial inventory is still ranked.
	locateWalkBudget = 4 * time.Second
	// locateBudget bounds the whole ranking, walk included.
	locateBudget = 12 * time.Second
	// locateQueryRunes bounds the question put to the backend.
	locateQueryRunes = 1500
)

// locateState is the session's cached inventory.
type locateState struct {
	mu  sync.Mutex
	inv *locate.Inventory
	at  time.Time
}

// locateCatalog lets the registry hold the Locate tool before the session
// that serves it exists.
type locateCatalog struct{ s *Session }

// Locate implements tools.Locator.
func (c *locateCatalog) Locate(ctx context.Context, question string, max int) tools.LocateResult {
	if c.s == nil {
		return tools.LocateResult{}
	}
	return c.s.locateFiles(ctx, question, max)
}

// locateEnabled reports whether the job may run.
func (s *Session) locateEnabled() bool {
	return s.jev != nil && s.jev.Enabled(config.JevExploreLocate)
}

// inventory returns the working directory's eligible files, walked at most
// once per locateInventoryTTL.
func (s *Session) inventory(ctx context.Context) *locate.Inventory {
	s.loc.mu.Lock()
	defer s.loc.mu.Unlock()
	if s.loc.inv != nil && time.Since(s.loc.at) < locateInventoryTTL {
		return s.loc.inv
	}
	wctx, cancel := context.WithTimeout(ctx, locateWalkBudget)
	defer cancel()
	inv, err := locate.Build(wctx, s.workdir)
	if inv == nil || err != nil && len(inv.Files) == 0 {
		return nil
	}
	s.loc.inv, s.loc.at = inv, time.Now()
	return inv
}

// locateRater adapts the backend to locate.Rater.
type locateRater struct {
	jobs     *jev.Jobs
	identity string
}

func (r *locateRater) Rate(ctx context.Context, st locate.Stage, question string, items []locate.Item) (map[string]float64, error) {
	li := make([]jev.LocateItem, len(items))
	for i, it := range items {
		li[i] = jev.LocateItem{ID: it.ID, Label: it.Label}
	}
	scores, res, err := r.jobs.RateLocate(ctx, string(st), question, li)
	if res.Identity != "" {
		r.identity = res.Identity
	}
	return scores, err
}

// rank runs a search and records it. ok is false when there is nothing to
// search.
func (s *Session) rank(ctx context.Context, question string, max int) (locate.Result, string, bool) {
	if !s.locateEnabled() {
		return locate.Result{}, "", false
	}
	ctx, cancel := context.WithTimeout(ctx, locateBudget)
	defer cancel()
	inv := s.inventory(ctx)
	if inv == nil {
		return locate.Result{}, "", false
	}
	start := time.Now()
	pref := ""
	if s.settings.Jev != nil {
		pref = s.settings.Jev.LocatePreviews
	}
	rater := &locateRater{jobs: s.jev}
	local, previews := false, false
	if s.jev.Client != nil {
		local = s.jev.Client.Local()
		previews = s.jev.Client.PreviewsAllowed(pref)
	}
	res := locate.Search(ctx, inv, sanitize.Clip(question, locateQueryRunes), locate.Options{
		Rater:    rater,
		Local:    local,
		Previews: previews,
		MaxHits:  max,
		Exclude: func(p string) bool {
			_, hit := s.flagged.lookup(filepath.Join(inv.Root, filepath.FromSlash(p)), inv.Root, inv.Root)
			return hit
		},
	})
	verdict := "ranked"
	if res.Lexical {
		verdict = "lexical"
	}
	rolemanager.RecordExploreLocate(verdict, len(res.Hits), res.DirsRated, res.FilesRated, res.Unknown, rater.identity, time.Since(start))
	by := "keywords"
	if !res.Lexical {
		by = shortModel(rater.identity)
	}
	return res, by, true
}

// locateFiles implements the Locate tool.
func (s *Session) locateFiles(ctx context.Context, question string, max int) tools.LocateResult {
	res, by, ok := s.rank(ctx, question, max)
	if !ok {
		return tools.LocateResult{}
	}
	out := tools.LocateResult{By: by}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, tools.LocateHit{Path: h.Path, Line: h.Line, Percent: int(h.Score*100 + 0.5), Lead: h.Lead})
	}
	return out
}

// seedTasks ranks the files for the goal and hands them to the tasks: each
// prompt names the best files, a task that needed no search loses a round,
// and a survey question the ranking found nothing for is dropped. The tasks
// come back unchanged when the job is off or nothing was found.
func (s *Session) seedTasks(ctx context.Context, tasks []explore.Task, goal string) []explore.Task {
	if len(tasks) == 0 {
		return tasks
	}
	res, _, ok := s.rank(ctx, goal, locate.DefaultMaxHits)
	if !ok || len(res.Hits) == 0 {
		return tasks
	}
	files := make([]explore.SeedFile, len(res.Hits))
	strong := false
	for i, h := range res.Hits {
		files[i] = explore.SeedFile{Path: h.Path, Line: h.Line, Lead: h.Lead}
		if !h.Lead && h.Rated {
			strong = true
		}
	}
	if !res.Lexical {
		tasks = explore.DropUnsupported(tasks, files)
	}
	line := explore.SeedLine(files)
	out := make([]explore.Task, len(tasks))
	for i, t := range tasks {
		out[i] = explore.WithSeed(t, line, strong)
	}
	return out
}

func shortModel(identity string) string {
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' {
			return identity[i+1:]
		}
	}
	if identity == "" {
		return "the backend"
	}
	return identity
}
