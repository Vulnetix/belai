package agent

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// Tool and skill selection. With deferred tools on, a request carries the core
// tools in full and the model loads the rest with ToolSearch; every skill's
// name and description also rides in the system block. A decision backend
// makes both cheaper and quicker: it rates the deferred tools and the skills
// against the user's request, so the likely ones are loaded before the model
// has to ask (one round trip fewer) and only the relevant skills are listed.
//
// Selection only narrows what is advertised, at the start of a turn, from the
// surface the mode already allows. Nothing is disabled: an unlisted skill is
// found by ToolSearch and loaded by the Skill tool, and a deferred tool still
// runs when called by name.
const (
	// maxPreloadTools is the most deferred tools loaded for a turn.
	maxPreloadTools = 6
	// maxListedSkills is the most skills listed for a turn.
	maxListedSkills = 5
	// poolLocal and poolRemote bound how many candidates are rated in one go:
	// the local model answers one question per request.
	poolLocal  = 24
	poolRemote = 200
)

// poolCap is how many candidates the backend is asked about.
func (s *Session) poolCap() int {
	if s.jev != nil && s.jev.Client != nil && s.jev.Client.Local() {
		return poolLocal
	}
	return poolRemote
}

// Skills implements tools.SkillCatalog: the installed skills a model may
// invoke, for ToolSearch to find.
func (s *Session) Skills() []tools.Candidate { return s.skillCands }

// Rank implements tools.SearchRanker: refine a keyword search with the
// decision backend. det is the deterministic top; the result drops what the
// backend rates below DropAt and adds what it rates at or above StrongAt. It
// returns ok false, leaving the deterministic list, when the job is off or the
// backend cannot answer.
func (s *Session) Rank(ctx context.Context, query string, det, pool []tools.Candidate, limit int) ([]tools.Candidate, bool) {
	if s.jev == nil || !s.jev.Enabled(config.JevToolSearch) || len(pool) == 0 {
		return nil, false
	}
	start := time.Now()
	rated := searchPool(query, det, pool, s.poolCap())
	items := make([]jev.SurfaceItem, len(rated))
	for i, c := range rated {
		items[i] = jev.SurfaceItem{ID: c.Key(), Name: c.Name, Description: firstSentence(c.Description)}
	}
	scores, res, err := s.jev.RankSearch(ctx, query, s.turnPrompt, items)
	if err != nil || len(scores) == 0 {
		return nil, false
	}
	merged := mergeSearch(det, rated, scores, limit)
	rolemanager.RecordToolSearch(len(det), len(merged), len(res.Unknown), res.Identity, time.Since(start))
	return merged, true
}

// searchPool picks the candidates the backend rates: the deterministic list
// first, then the rest by how well they match the query, up to cap.
func searchPool(query string, det, pool []tools.Candidate, cap int) []tools.Candidate {
	out := make([]tools.Candidate, 0, min(cap, len(pool)))
	seen := map[string]bool{}
	add := func(c tools.Candidate) {
		if len(out) < cap && !seen[c.Key()] {
			seen[c.Key()] = true
			out = append(out, c)
		}
	}
	for _, c := range det {
		add(c)
	}
	for _, c := range tools.RankCandidates(pool, query) {
		add(c)
	}
	rest := append([]tools.Candidate(nil), pool...)
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	for _, c := range rest {
		add(c)
	}
	return out
}

// mergeSearch applies the ToolSearch rule to the deterministic list: an entry
// the backend rates below DropAt goes, an entry it did not rate stays, and any
// other candidate rated at or above StrongAt is added. The result is ordered by
// the backend's score, an unrated entry counting as KeepAt, with ties in the
// deterministic order, and is capped at limit.
func mergeSearch(det, pool []tools.Candidate, scores map[string]float64, limit int) []tools.Candidate {
	type entry struct {
		c     tools.Candidate
		score float64
		rank  int
	}
	var es []entry
	seen := map[string]bool{}
	for i, c := range det {
		sc, ok := scores[c.Key()]
		if ok && sc < config.ActiveJevThresholds().DropAt {
			continue
		}
		if !ok {
			sc = config.ActiveJevThresholds().KeepAt
		}
		seen[c.Key()] = true
		es = append(es, entry{c, sc, i})
	}
	for i, c := range pool {
		if seen[c.Key()] {
			continue
		}
		if sc, ok := scores[c.Key()]; ok && sc >= config.ActiveJevThresholds().StrongAt {
			seen[c.Key()] = true
			es = append(es, entry{c, sc, len(det) + i})
		}
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].score != es[j].score {
			return es[i].score > es[j].score
		}
		return es[i].rank < es[j].rank
	})
	if limit > 0 && len(es) > limit {
		es = es[:limit]
	}
	out := make([]tools.Candidate, len(es))
	for i, e := range es {
		out[i] = e.c
	}
	return out
}

// selectSurface chooses which skills the system block lists for a turn and
// loads the deferred tools the request is likely to need. skills are the
// installed skills a model may invoke; the returned list is the ones to list
// and more is how many were left out. With the job off, no backend, or a
// failure, every skill is listed and nothing is loaded, exactly as without it.
func (s *Session) selectSurface(ctx context.Context, request, mode string, skills []tools.Candidate) (listed []tools.Candidate, more int) {
	pool, ok := s.surfacePool(request, skills)
	if !ok {
		return skills, 0
	}
	return s.applySurface(request, skills, s.rateSurface(ctx, request, mode, pool))
}

// surfaceTimeout bounds the tool-selection rating, like the other pre-prompt
// decision jobs: a verdict is worth having only before the request goes out.
const surfaceTimeout = 8 * time.Second

// surfaceRating is one rating of a candidate pool.
type surfaceRating struct {
	pool   []tools.Candidate
	rated  []tools.Candidate
	scores map[string]float64
	model  string
	err    error
	start  time.Time
}

// surfacePool is what tool selection rates: the deferred tools of the current
// surface plus the skills. ok is false when the job does not run. It reads
// the session's surface, so it runs on the turn's goroutine.
func (s *Session) surfacePool(request string, skills []tools.Candidate) ([]tools.Candidate, bool) {
	if s.jev == nil || !s.jev.Enabled(config.JevToolSelection) || s.deferral == nil ||
		strings.TrimSpace(request) == "" {
		return nil, false
	}
	if _, ok := s.registry.Find(tools.ToolSearchName); !ok {
		// Without ToolSearch an unlisted skill would be unreachable.
		return nil, false
	}
	var pool []tools.Candidate
	for _, d := range s.Deferred() {
		pool = append(pool, tools.Candidate{Name: d.Name, Description: d.Description})
	}
	pool = append(pool, skills...)
	return pool, len(pool) > 0
}

// rateSurface asks the backend about a pool. It touches no session state but
// the decision client, so it may run ahead of the turn (agent.go starts it as
// soon as the mode is known).
func (s *Session) rateSurface(ctx context.Context, request, mode string, pool []tools.Candidate) surfaceRating {
	r := surfaceRating{pool: pool, start: time.Now()}
	r.rated = searchPool(request, nil, pool, s.poolCap())
	items := make([]jev.SurfaceItem, len(r.rated))
	for i, c := range r.rated {
		items[i] = jev.SurfaceItem{ID: c.Key(), Name: c.Name, Description: firstSentence(c.Description)}
	}
	ctx, cancel := context.WithTimeout(ctx, surfaceTimeout)
	defer cancel()
	var res jev.ScoreResult
	r.scores, res, r.err = s.jev.RateSurface(ctx, request, mode, items)
	r.model = res.Identity
	return r
}

// samePool reports whether two pools hold the same candidates in order.
func samePool(a, b []tools.Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key() != b[i].Key() {
			return false
		}
	}
	return true
}

// applySurface loads the tools a rating chose and returns the skills to list.
func (s *Session) applySurface(request string, skills []tools.Candidate, r surfaceRating) (listed []tools.Candidate, more int) {
	start := r.start
	if r.err != nil || len(r.scores) == 0 {
		rolemanager.RecordToolSelect("fallback", 0, len(skills), 0, 0, r.model, time.Since(start))
		return skills, 0
	}
	rated, scores, res := r.rated, r.scores, jev.ScoreResult{Identity: r.model}
	preload, kept := chooseSurface(request, rated, scores)
	loaded := s.Load(preload)
	for _, c := range skills {
		if kept[c.Key()] {
			listed = append(listed, c)
		}
	}
	more = len(skills) - len(listed)
	saved := 0
	for _, c := range skills {
		if !kept[c.Key()] {
			saved += len(c.Name) + len(c.Description) + 4
		}
	}
	rolemanager.RecordToolSelect("selected", len(loaded), len(listed), more, saved/4, res.Identity, time.Since(start))
	return listed, more
}

// chooseSurface reads the scores: the deferred tools to preload (rated at or
// above KeepAt, best first, at most maxPreloadTools) and the skills to list
// (the same rule, at most maxListedSkills, plus any skill the request names).
func chooseSurface(request string, rated []tools.Candidate, scores map[string]float64) (preload []string, kept map[string]bool) {
	type sc struct {
		c     tools.Candidate
		score float64
	}
	var toolsHit, skillsHit []sc
	for _, c := range rated {
		if s, ok := scores[c.Key()]; ok && s >= config.ActiveJevThresholds().KeepAt {
			if c.Skill {
				skillsHit = append(skillsHit, sc{c, s})
			} else {
				toolsHit = append(toolsHit, sc{c, s})
			}
		}
	}
	byScore := func(x []sc) {
		sort.SliceStable(x, func(i, j int) bool { return x[i].score > x[j].score })
	}
	byScore(toolsHit)
	byScore(skillsHit)
	kept = map[string]bool{}
	for i, h := range toolsHit {
		if i < maxPreloadTools {
			preload = append(preload, h.c.Name)
		}
	}
	for i, h := range skillsHit {
		if i < maxListedSkills {
			kept[h.c.Key()] = true
		}
	}
	// A skill the request names outright is always listed.
	lower := strings.ToLower(sanitize.Sanitize(request))
	for _, c := range rated {
		if c.Skill && len(c.Name) >= 3 && strings.Contains(lower, strings.ToLower(c.Name)) {
			kept[c.Key()] = true
		}
	}
	return preload, kept
}

// modelSkills are the installed skills a model may invoke, as candidates. A
// user-only skill is not one.
func (s *Session) modelSkills() []tools.Candidate {
	var all []tools.Candidate
	for _, e := range tools.SkillEntries(s.registry) {
		if !e.DisableModelInvocation {
			all = append(all, tools.Candidate{Name: e.Name, Description: sanitize.Line(e.Description, 1024), Skill: true})
		}
	}
	return all
}
