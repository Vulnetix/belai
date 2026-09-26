package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/permissions"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/readindex"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
)

// Context prefetch. A plan or goal turn nearly always starts by reading the
// repository's agent instruction files and the files the working tree has
// changed, and each of those reads costs the model a full round trip. The
// harness already knows the list — the repo map names the instruction files
// and RefreshStatus lists the changed paths — so it reads them up front,
// through the session's own Read tool and the same gate every Read result
// passes, and hands the admitted ones over as sealed file attachments on the
// user turn. They are recorded in the read index against the attachment body,
// so a Read of the same unchanged file this turn gets a pointer, not the bytes
// again. Nothing here enters the system block, and a withheld file is flagged
// exactly as a withheld Read would be.

// Prefetch bounds.
const (
	prefetchMaxFiles    = 12
	prefetchFileBytes   = 48 * 1024  // a larger file is left for the model to page through
	prefetchTotalBytes  = 128 * 1024 // on-disk bytes across every prefetched file
	prefetchConcurrency = 4
	prefetchBudget      = 30 * time.Second
	// prefetchCallID marks read-index entries whose bytes rode on an attachment.
	prefetchCallID = "harness-prefetch"
)

// forgeMaxAge is how old the shared forge snapshot may be before a turn asks
// for a background refresh; forgeMaxShow is the age past which it is too
// stale to put in front of the model at all.
const (
	forgeMaxAge  = time.Minute
	forgeMaxShow = 10 * time.Minute
)

// prefetchResult is one admitted file.
type prefetchResult struct {
	att run.Attachment
	key readindex.Key
}

// pendingPrefetch is a prefetch running alongside exploration.
type pendingPrefetch struct {
	done chan struct{}
	out  []prefetchResult
}

// wait blocks until the prefetch finishes and returns the admitted files in
// candidate order. A nil pending prefetch returns nothing.
func (p *pendingPrefetch) wait() []prefetchResult {
	if p == nil {
		return nil
	}
	<-p.done
	return p.out
}

// startPrefetch begins reading and classifying the prefetch candidates in the
// background. seed lists root-relative paths that should be prefetched first
// (e.g. files named in a plan-handoff); skip names root-relative paths the
// user already attached.
func (s *Session) startPrefetch(ctx context.Context, pipe *rolemanager.Pipeline, history []run.Turn, seed, skip map[string]bool, emit func(Event)) *pendingPrefetch {
	tool, _ := s.execTool("Read")
	if tool == nil {
		return nil
	}
	cands := s.prefetchCandidates(ctx, tool, history, skip)
	if len(seed) > 0 {
		seen := map[string]bool{}
		for _, c := range cands {
			seen[c] = true
		}
		var merged []string
		for p := range seed {
			p = filepath.ToSlash(filepath.Clean(p))
			if p == "." || p == ".." || strings.HasPrefix(p, "../") || filepath.IsAbs(p) || seen[p] || skip[p] {
				continue
			}
			seen[p] = true
			merged = append(merged, p)
		}
		cands = append(merged, cands...)
	}
	if len(cands) == 0 {
		return nil
	}
	p := &pendingPrefetch{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		start := time.Now()
		p.out = s.runPrefetch(ctx, pipe, tool, cands)
		s.trace.Event("agent", "prefetch", time.Since(start))
		if len(p.out) > 0 {
			paths := make([]string, len(p.out))
			for i, r := range p.out {
				paths[i] = r.att.Label
			}
			emit(Event{Kind: EventPrefetchKind, Paths: paths})
		}
	}()
	return p
}

// prefetchCandidates lists root-relative paths to prefetch: the agent
// instruction files first, then the changed paths. It skips anything deleted,
// not a regular file, empty, too large, already live in the conversation,
// withheld earlier in the session, or not allowed a Read by the permission
// rules without asking.
func (s *Session) prefetchCandidates(ctx context.Context, tool tools.Tool, history []run.Turn, skip map[string]bool) []string {
	if s.repoMap == nil || s.registry == nil || s.registry.Cwd() == nil {
		return nil
	}
	root := s.registry.Cwd().Root()
	m := *s.repoMap
	m.RefreshStatus(ctx)

	var raw []string
	for _, f := range m.AgentsFiles {
		raw = append(raw, f.Name)
	}
	for _, c := range m.Changed {
		if strings.Contains(c.Status, "D") {
			continue
		}
		p := c.Path
		if _, after, ok := strings.Cut(p, " -> "); ok {
			p = after // a rename lists "old -> new"
		}
		raw = append(raw, strings.Trim(p, `"`))
	}

	seen := map[string]bool{}
	var out []string
	var total int64
	for _, rel := range raw {
		if strings.HasSuffix(rel, "/") {
			continue // an untracked directory
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." || seen[rel] || skip[rel] || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		seen[rel] = true
		abs := filepath.Join(root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > prefetchFileBytes || total+fi.Size() > prefetchTotalBytes {
			continue
		}
		if _, withheld := s.flagged.lookup(abs, "", ""); withheld {
			continue
		}
		if s.reads != nil {
			if _, live := s.reads.Lookup(readindex.Key{Path: abs}, func(e readindex.Entry) bool { return liveResult(history, e.ResultHash) }); live {
				continue
			}
		}
		if dec, _, _ := s.decidePermission("Read", tool.Subject(map[string]any{"file_path": rel})); dec != permissions.DecisionAllow {
			continue
		}
		total += fi.Size()
		out = append(out, rel)
		if len(out) == prefetchMaxFiles {
			break
		}
	}
	return out
}

// runPrefetch reads and gates the candidates, a few at a time, records the
// admitted ones in the read index, and returns them in candidate order.
func (s *Session) runPrefetch(ctx context.Context, pipe *rolemanager.Pipeline, tool tools.Tool, cands []string) []prefetchResult {
	ctx, cancel := context.WithTimeout(ctx, prefetchBudget)
	defer cancel()
	results := make([]*prefetchResult, len(cands))
	sem := make(chan struct{}, prefetchConcurrency)
	var wg sync.WaitGroup
	for i, rel := range cands {
		wg.Add(1)
		go func(i int, rel string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.prefetchOne(ctx, pipe, tool, rel)
		}(i, rel)
	}
	wg.Wait()
	var out []prefetchResult
	for _, r := range results {
		if r == nil {
			continue
		}
		out = append(out, *r)
		s.recordRead(r.key, prefetchCallID, r.att.Body)
	}
	return out
}

// prefetchOne reads one file and gates it exactly as executeCall gates a Read
// result: sanitised only when the posture ignores the gate (the classifier is
// then not called at all), classified otherwise, and flagged when withheld.
func (s *Session) prefetchOne(ctx context.Context, pipe *rolemanager.Pipeline, tool tools.Tool, rel string) *prefetchResult {
	res, err := tool.Execute(ctx, map[string]any{"file_path": rel})
	if err != nil || strings.TrimSpace(res.Content) == "" {
		return nil
	}
	abs, _ := res.Meta["abs"].(string)
	if abs == "" {
		abs = filepath.Join(s.registry.Cwd().Root(), filepath.FromSlash(rel))
	}
	key := readindex.Key{Path: abs}
	if s.live.Level(posture.ToolResultUnsafe) == posture.Ignore {
		return &prefetchResult{att: run.Attachment{Kind: "file", Label: rel, Body: sanitize.Sanitize(res.Content)}, key: key}
	}
	dec, err := pipe.Process(ctx, res)
	if err != nil {
		return nil
	}
	if dec.Action != rolemanager.ActionProceed {
		s.flagged.flag(res, dec.Sentinel)
		s.verdictWithheld.Add(1)
		return nil
	}
	return &prefetchResult{att: run.Attachment{Kind: "file", Label: rel, Body: dec.Content}, key: key}
}

// prefetchDirective tells the model which files ride on this turn, so its
// first move is the work rather than reading them. Paths only.
func prefetchDirective(rs []prefetchResult) string {
	if len(rs) == 0 {
		return ""
	}
	paths := make([]string, len(rs))
	for i, r := range rs {
		paths[i] = sanitize.Sanitize(r.att.Label)
	}
	sort.Strings(paths)
	return "Harness-attached for this turn (the repository's agent instruction files and the working tree's changed files, read and classified exactly like Read results and current as of this turn; use them instead of reading these files again): " + strings.Join(paths, ", ")
}

// attachedPaths returns the root-relative paths of the user's file
// attachments, so the prefetch does not send a file twice.
func attachedPaths(atts []run.Attachment) map[string]bool {
	out := map[string]bool{}
	for _, a := range atts {
		if a.Kind != "file" {
			continue
		}
		p := strings.TrimPrefix(strings.TrimSpace(a.Label), "@")
		out[filepath.ToSlash(filepath.Clean(strings.TrimPrefix(p, "/")))] = true
	}
	return out
}

// forgeStatus renders the shared forge snapshot's facts for this turn and
// asks for a background refresh when it is getting old. It never blocks on
// the network: a turn uses whatever snapshot is already cached.
func (s *Session) forgeStatus() string {
	if s.forge == nil || s.registry == nil || s.registry.Cwd() == nil {
		return ""
	}
	dir := s.registry.Cwd().Dir()
	snap, at, ok := s.forge.Get(dir)
	s.forge.RefreshAsync(dir, forgeMaxAge, nil)
	if !ok {
		return ""
	}
	age := time.Since(at)
	if age > forgeMaxShow {
		return ""
	}
	return prompt.ForgeStatusBlock(snap, age)
}
