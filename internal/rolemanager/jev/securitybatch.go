package jev

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// Security checks against a SystemOne decision backend are batched: checks
// that arrive within batchLinger of each other (the results of one pass's
// parallel tool calls, a prefetch, attachments, session-start doc warming) go
// out as one request, one noul question per item and category, instead of
// one request each. docs/role-manager.md, "Security classification".
const (
	// batchLinger is how long the first check of a batch waits for others.
	batchLinger = 8 * time.Millisecond
	// batchMaxItems is the most items one request carries.
	batchMaxItems = 250
	// batchMaxBytes bounds the content one request carries, the size of one
	// default Read, so a batch never asks the backend to read more than a
	// single check could. A larger item travels alone.
	batchMaxBytes = 64 << 10
)

// batchItem is one waiting check.
type batchItem struct {
	content string
	cats    []rolemanager.Sentinel
	done    chan batchAnswer
}

type batchAnswer struct {
	answers map[string]decisions.Answer
	err     error
}

// securityBatcher gathers the checks for one backend.
type securityBatcher struct {
	backend      decisions.Decider
	maxQuestions int
	maxBytes     int

	mu      sync.Mutex
	pending []*batchItem
	bytes   int
	timer   *time.Timer
}

var batchers sync.Map // backend identity -> *securityBatcher

// batcherFor returns the batcher for a backend. Pipelines are built per call,
// so the batcher is shared by identity; the latest backend instance sends.
func batcherFor(d decisions.Decider) *securityBatcher {
	b := &securityBatcher{backend: d, maxBytes: batchMaxBytes}
	if s, ok := d.(*decisions.SystemOne); ok {
		b.maxQuestions = s.MaxQuestions
		if s.MaxBodyBytes > 0 {
			// Leave room for the question text and the JSON around it.
			b.maxBytes = min(b.maxBytes, s.MaxBodyBytes/2)
		}
	}
	if v, ok := batchers.Load(d.Identity()); ok {
		old := v.(*securityBatcher)
		old.mu.Lock()
		old.backend, old.maxQuestions, old.maxBytes = b.backend, b.maxQuestions, b.maxBytes
		old.mu.Unlock()
		return old
	}
	v, _ := batchers.LoadOrStore(d.Identity(), b)
	return v.(*securityBatcher)
}

// batchable reports whether a backend's checks are batched: only a SystemOne
// server answers many questions in one forward pass. A local letter reader
// asks them one at a time, so batching would only add the linger.
func batchable(d decisions.Decider) bool {
	return d != nil && d.Backend() == decisions.BackendSystemOne
}

// decide queues one check and waits for its answers, keyed by category as a
// single request's would be.
func (b *securityBatcher) decide(ctx context.Context, content string, cats []rolemanager.Sentinel) (map[string]decisions.Answer, error) {
	if len(content) > b.maxBytes || (b.maxQuestions > 0 && len(cats) > b.maxQuestions) {
		return b.single(ctx, content, cats)
	}
	it := &batchItem{content: content, cats: cats, done: make(chan batchAnswer, 1)}
	b.mu.Lock()
	if len(b.pending) > 0 && !b.fits(it) {
		b.flushLocked()
	}
	b.pending = append(b.pending, it)
	b.bytes += len(content)
	switch {
	case !b.fits(&batchItem{cats: cats}):
		b.flushLocked()
	case len(b.pending) == 1:
		b.timer = time.AfterFunc(batchLinger, b.flush)
	}
	b.mu.Unlock()
	select {
	case a := <-it.done:
		return a.answers, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fits reports whether one more item like it fits the pending batch.
func (b *securityBatcher) fits(it *batchItem) bool {
	if len(b.pending) >= batchMaxItems || b.bytes+len(it.content) > b.maxBytes {
		return false
	}
	if b.maxQuestions > 0 {
		n := len(it.cats)
		for _, p := range b.pending {
			n += len(p.cats)
		}
		return n <= b.maxQuestions
	}
	return true
}

func (b *securityBatcher) flush() {
	b.mu.Lock()
	b.flushLocked()
	b.mu.Unlock()
}

func (b *securityBatcher) flushLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	items := b.pending
	b.pending, b.bytes = nil, 0
	if len(items) == 0 {
		return
	}
	go b.send(items)
}

// send asks one batch and hands each item its answers. A batch where any item
// is not plainly SAFE is not trusted for the others: an injection in one item
// could sway the answers about its neighbours, so each of them is asked again
// on its own. A failed batch is asked item by item.
func (b *securityBatcher) send(items []*batchItem) {
	ctx := context.Background()
	if len(items) == 1 {
		a, err := b.single(ctx, items[0].content, items[0].cats)
		items[0].done <- batchAnswer{answers: a, err: err}
		return
	}
	res, err := b.backend.Decide(ctx, buildBatchRequest(items))
	if err != nil {
		b.each(ctx, items)
		return
	}
	per := make([]map[string]decisions.Answer, len(items))
	trusted := true
	for i, it := range items {
		per[i] = make(map[string]decisions.Answer, len(it.cats))
		for _, cat := range it.cats {
			if a, ok := res.Answers[batchQuestionID(i, cat)]; ok {
				per[i][string(cat)] = a
			}
		}
		if v, decided := thresholdCategories(per[i], it.cats); !decided || v != string(rolemanager.SentinelSafe) {
			trusted = false
		}
	}
	if !trusted {
		// The item that was flagged or undecided keeps its own answers; the
		// rest are asked alone.
		var again []*batchItem
		for i, it := range items {
			if v, decided := thresholdCategories(per[i], it.cats); decided && v == string(rolemanager.SentinelSafe) {
				again = append(again, it)
				continue
			}
			it.done <- batchAnswer{answers: per[i]}
		}
		b.each(ctx, again)
		return
	}
	for i, it := range items {
		it.done <- batchAnswer{answers: per[i]}
	}
}

// each asks every item alone, concurrently.
func (b *securityBatcher) each(ctx context.Context, items []*batchItem) {
	var wg sync.WaitGroup
	for _, it := range items {
		wg.Add(1)
		go func(it *batchItem) {
			defer wg.Done()
			a, err := b.single(ctx, it.content, it.cats)
			it.done <- batchAnswer{answers: a, err: err}
		}(it)
	}
	wg.Wait()
}

// single is the unbatched request: the content in state.content and one
// question per category, keyed by category.
func (b *securityBatcher) single(ctx context.Context, content string, cats []rolemanager.Sentinel) (map[string]decisions.Answer, error) {
	b.mu.Lock()
	backend := b.backend
	b.mu.Unlock()
	res, err := backend.Decide(ctx, singleSecurityRequest(content, cats))
	return res.Answers, err
}

func singleSecurityRequest(content string, cats []rolemanager.Sentinel) decisions.Request {
	questions := make(map[string]decisions.Question, len(cats))
	for _, cat := range cats {
		questions[string(cat)] = decisions.Noul(categoryProposition(cat))
	}
	return decisions.Request{Questions: questions, State: map[string]any{"content": content}}
}

// batchQuestionID keys one item's question about one category.
func batchQuestionID(i int, cat rolemanager.Sentinel) string {
	return strconv.Itoa(i) + ":" + string(cat)
}

// buildBatchRequest lays a batch out as state.items and one question per item
// and category, each naming the item it is about.
func buildBatchRequest(items []*batchItem) decisions.Request {
	contents := make(map[string]string, len(items))
	n := 0
	for _, it := range items {
		n += len(it.cats)
	}
	questions := make(map[string]decisions.Question, n)
	for i, it := range items {
		id := strconv.Itoa(i)
		contents[id] = it.content
		for _, cat := range it.cats {
			questions[batchQuestionID(i, cat)] = decisions.Noul(
				"Consider only state.items[" + strconv.Quote(id) + "]. " + categoryProposition(cat))
		}
	}
	return decisions.Request{State: map[string]any{"items": contents}, Questions: questions}
}
