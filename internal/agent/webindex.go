package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/kbgate"
	"github.com/vulnetix/belai/internal/offload"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/tools"
)

// webIndex is the session's search index over the pages WebFetch returned
// (docs/web-fetch.md). It is the tools.FetchedIndex behind SearchFetched.
//
// It reuses the knowledge package's in-memory index, chunker and ranking, and
// the same ingestion rule: a page reaches it only after its WebFetch result was
// admitted, every chunk is sanitised and then admitted by the gate (the
// security classifier, as KindWebFetch text), a flagged chunk is never stored
// and a gate error stores nothing for the page. A search is a lookup: it calls
// no model and makes no request. Nothing is written to disk.
//
// Classifying a page's chunks can take several classifier round trips, so
// Add returns at once and the page becomes searchable when its chunks have been
// admitted. One page is ingested at a time, and a small queue bounds the work.
type webIndex struct {
	ix   *knowledge.Index
	gate knowledge.Gate
	// level reads the live posture's tool-result level; perms the session's
	// permission rules. Both are read on every use, so a change of posture or
	// a new rule applies to what is already indexed.
	level func() posture.Level
	perms func() permissions.Settings

	capTokens  int // whole index
	pageTokens int // one page
	resultCap  int // one search's answer
	timeout    time.Duration

	run sync.Mutex // serialises ingestion: Index.Ingest calls must not overlap
	wg  sync.WaitGroup

	mu       sync.Mutex
	queued   int
	rejected map[string]int  // URL -> times Remove was called
	ungated  map[string]bool // URLs indexed while the classifier was off
}

// webIndexQueue is how many pages may wait to be classified. A page that finds
// the queue full is simply not indexed; it was still returned to the model.
const webIndexQueue = 4

// webIndexPageTokens bounds how much of one page is indexed.
const webIndexPageTokens = 12000

func newWebIndex(gate knowledge.Gate, level func() posture.Level, perms func() permissions.Settings, capTokens, resultTokens int) *webIndex {
	w := &webIndex{
		ix:         knowledge.NewIndex("web"),
		gate:       gate,
		level:      level,
		perms:      perms,
		capTokens:  capTokens,
		pageTokens: min(webIndexPageTokens, capTokens),
		resultCap:  resultTokens,
		timeout:    2 * time.Minute,
		rejected:   map[string]int{},
		ungated:    map[string]bool{},
	}
	return w
}

// Wait blocks until every page handed to Add has been ingested or dropped.
func (w *webIndex) Wait() { w.wg.Wait() }

func webAddress(url string) string {
	h := sha256.Sum256([]byte(url))
	return knowledge.Address("web", hex.EncodeToString(h[:8]))
}

// Add queues an admitted page for indexing.
func (w *webIndex) Add(ctx context.Context, url, text string) {
	w.mu.Lock()
	if w.queued >= webIndexQueue {
		w.mu.Unlock()
		return
	}
	w.queued++
	rej := w.rejected[url]
	// The posture is read before the classifier runs and again after: a page
	// whose chunks were admitted while guardrails were off was never
	// classified, and is marked so it is not served once they are on.
	off := w.level() == posture.Ignore
	w.mu.Unlock()
	w.wg.Add(1)
	// The turn may end before the classifier has answered, so the work does not
	// share the turn's cancellation; it is bounded by its own timeout.
	bg := context.WithoutCancel(ctx)
	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			w.queued--
			w.mu.Unlock()
		}()
		w.ingest(bg, url, text, rej, off)
	}()
}

func (w *webIndex) ingest(ctx context.Context, url, text string, rej int, off bool) {
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	w.run.Lock()
	defer w.run.Unlock()

	addr := webAddress(url)
	chunks := capChunks(knowledge.SplitText(text), w.pageTokens)
	if len(chunks) == 0 {
		return
	}
	// Ingest keeps an unchanged page without gating it again. A page that was
	// admitted while the classifier was off was never classified, so with the
	// classifier on it is dropped first and ingested afresh.
	w.mu.Lock()
	regate := w.ungated[url] && !off && w.level() != posture.Ignore
	w.mu.Unlock()
	if regate {
		w.ix.Remove(addr)
	}
	w.makeRoom()
	data := []byte(text)
	sum := sha256.Sum256(data)
	_, err := w.ix.Ingest(ctx, knowledge.Input{
		Address: addr, Source: url, Size: int64(len(data)), ModTime: time.Now(),
		SHA: hex.EncodeToString(sum[:]), Chunks: chunks,
	}, w.gate, w.capTokens)
	if err != nil {
		// A gate or cap error stores nothing for the page (Ingest changes
		// nothing on error): fail closed.
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.rejected[url] != rej {
		// The page's result was withheld while it was being indexed.
		w.ix.Remove(addr)
		return
	}
	if off || w.level() == posture.Ignore {
		w.ungated[url] = true
	} else {
		delete(w.ungated, url)
	}
}

// makeRoom drops the oldest pages until one more page fits.
func (w *webIndex) makeRoom() {
	for w.ix.Tokens()+w.pageTokens > w.capTokens {
		docs := w.ix.Docs()
		if len(docs) == 0 {
			return
		}
		w.ix.Remove(docs[0].Address)
		w.mu.Lock()
		delete(w.ungated, docs[0].Source)
		w.mu.Unlock()
	}
}

// capChunks keeps the leading chunks that fit within tokens.
func capChunks(in []knowledge.RawChunk, tokens int) []knowledge.RawChunk {
	used := 0
	for i, c := range in {
		used += offload.Tokens(c.Text)
		if used > tokens {
			return in[:i]
		}
	}
	return in
}

// Remove drops a page and stops one still being indexed from landing.
func (w *webIndex) Remove(url string) {
	w.mu.Lock()
	w.rejected[url]++
	delete(w.ungated, url)
	w.mu.Unlock()
	w.ix.Remove(webAddress(url))
}

// Search returns the best passages. A page a WebFetch deny rule now blocks is
// not offered, and neither is one indexed while guardrails were off once they
// are on again.
func (w *webIndex) Search(query string) []tools.FetchedHit {
	guarded := w.level() != posture.Ignore
	perms := w.perms()
	w.mu.Lock()
	ungated := make(map[string]bool, len(w.ungated))
	for k, v := range w.ungated {
		ungated[k] = v
	}
	w.mu.Unlock()
	hits := knowledge.NewSet(w.ix).Search(query, w.resultCap, func(h knowledge.Hit) bool {
		if h.Label {
			return false
		}
		if guarded && ungated[h.Source] {
			return false
		}
		if dec, rule := perms.Explain("WebFetch", h.Source); rule != "" && dec == permissions.DecisionBlock {
			return false
		}
		return true
	})
	out := make([]tools.FetchedHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, tools.FetchedHit{URL: h.Source, Start: h.Start, End: h.End, Text: h.Text, Score: h.Score})
	}
	return out
}

// installWebPages switches the registry's page store on for this session: the
// cache, and the index behind SearchFetched. A nil store (a registry without
// one) leaves WebFetch as it was.
func (s *Session) installWebPages(p *tools.WebPages) {
	if p == nil {
		return
	}
	if s.settings.WebFetchCacheEnabled() {
		p.EnableCache(tools.WebPagesConfig{TTL: time.Duration(s.settings.WebFetchCacheTTLSeconds()) * time.Second})
	}
	if !s.settings.WebFetchIndexEnabled() {
		return
	}
	_, _, result := s.settings.KnowledgeLimits()
	// Chunks are classified as WebFetch text, through the same pipeline a
	// WebFetch result takes; the level is checked before the classifier is
	// called, so guardrails off sends nothing.
	gate := kbgate.New(s.cfg, s.client, s.cache, s.live, tools.KindWebFetch)
	w := newWebIndex(gate,
		func() posture.Level { return s.live.Level(posture.ToolResultUnsafe) },
		func() permissions.Settings { return s.perms },
		s.settings.WebFetchIndexTokens(), result)
	s.webIndex = w
	p.SetIndex(w)
}

// settleFetched tells the page store how the result behind ref was judged. An
// admitted page is cached and handed to the index; a withheld one is evicted
// from both, so nothing flagged is ever served or searched again. It runs after
// the gate, never before.
func (s *Session) settleFetched(ctx context.Context, ref tools.FetchRef, admitted bool) {
	pages := s.registry.WebPages()
	if pages == nil {
		return
	}
	if !admitted {
		pages.Flagged(ref)
		return
	}
	if text, ok := pages.Admitted(ref); ok {
		if ix := pages.Index(); ix != nil {
			ix.Add(ctx, ref.URL, text)
		}
	}
}
