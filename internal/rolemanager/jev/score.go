package jev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/sanitize"
)

// The thresholds every relevance job reads its scores against. A job may
// override one through settings; these are the defaults.
const (
	// DropAt: an item scoring below this is dropped from a list it was on.
	DropAt = 0.10
	// KeepAt: an item scoring at or above this is kept.
	KeepAt = 0.50
	// StrongAt: an item scoring at or above this is added to a list even when
	// no other signal picked it.
	StrongAt = 0.80
	// SwapAt: a single candidate at or above this replaces the call it rates.
	SwapAt = 0.95
)

// Batch limits. A remote backend takes many questions in one request; the
// local model answers one question per llama request, so its batches are small
// and its state is capped at decisions.DefaultMaxStateBytes.
const (
	maxItemsRemote  = 128
	maxBytesRemote  = 38_000
	maxItemsLocal   = 24
	maxBytesLocal   = 12_000
	maxSplitDepth   = 6
	defaultRequests = 64
	// itemOverhead is what the batcher charges each item beyond its id and label.
	itemOverhead = 160
	// scoreWorkers is the number of batches in flight against a remote backend.
	scoreWorkers = 8
)

// ScoreItem is one candidate to be scored.
type ScoreItem struct {
	// ID is unique within a request; it is the question key.
	ID string
	// Label describes the item to the backend. It is a DecisionText, so only
	// sanitize.ForDecision can produce it.
	Label sanitize.DecisionText
}

// ScoreRequest asks how well each item fits a criterion for a context.
type ScoreRequest struct {
	// Job names the caller (prune, tool_select, ...) for records and the cache.
	Job string
	// Criterion is the shared statement every item is judged against, sent
	// once in the state; per-item questions only point at it.
	Criterion sanitize.DecisionText
	// Context is what the criterion is judged against: the user's request, the
	// goal, the command being rated.
	Context sanitize.DecisionText
	// Extra is further named facts (mode, recent tools). Keys are harness
	// constants; values are sanitised.
	Extra map[string]sanitize.DecisionText
	// Items are the candidates.
	Items []ScoreItem
	// MaxRequests bounds the requests one call may make; zero means 64.
	MaxRequests int
	// NoCache keeps the call out of the score cache: for a context that is
	// never asked about twice, so its answers would only crowd out others.
	NoCache bool
}

// ScoreResult is what came back.
type ScoreResult struct {
	// Scores holds the valid answer of every item that got one.
	Scores map[string]float64
	// Unknown lists items with no valid answer, in request order. Callers fall
	// back to their deterministic result for these; an unknown item is not a
	// low score.
	Unknown []string
	// Requests is the number of decision requests made (cache hits excluded).
	Requests int
	// CacheHits counts items answered from the cache.
	CacheHits int
	// Splits counts batches that were halved after a failure.
	Splits int
	// Identity names the backend and model.
	Identity string
	// Backend is the transport.
	Backend decisions.Backend
	// Latency is the wall time of the call.
	Latency time.Duration
	// FailClass is the class of the first failure that left items unknown, or
	// "" when every item was answered.
	FailClass decisions.Class
}

// ErrScoreAuth is returned when the backend refused the credential; no items
// were scored and the caller should treat the job as unavailable.
var ErrScoreAuth = errors.New("decision backend refused the credential")

// ScoreCache remembers scores for the life of a session. Keys hash the job,
// backend identity, criterion, context, extra facts and the item, so a change
// to any of them is a miss. Only numbers are stored.
type ScoreCache struct {
	mu  sync.Mutex
	max int
	m   map[string]cacheEntry
}

type cacheEntry struct {
	score float64
	at    time.Time
}

// NewScoreCache builds a cache holding at most max entries (zero means 4096).
func NewScoreCache(max int) *ScoreCache {
	if max <= 0 {
		max = 4096
	}
	return &ScoreCache{max: max, m: map[string]cacheEntry{}}
}

const scoreTTL = 30 * time.Minute

func (c *ScoreCache) get(key string) (float64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > scoreTTL {
		delete(c.m, key)
		return 0, false
	}
	return e.score, true
}

func (c *ScoreCache) put(key string, score float64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.max {
		// Drop the oldest quarter; the cache is a convenience, not a record.
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(c.m))
		for k, e := range c.m {
			all = append(all, kv{k, e.at})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, e := range all[:len(all)/4+1] {
			delete(c.m, e.k)
		}
	}
	c.m[key] = cacheEntry{score: score, at: time.Now()}
}

// Len is the number of remembered scores.
func (c *ScoreCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// SetScoreCache attaches a cache to the client. Without one every call asks the
// backend.
func (c *Client) SetScoreCache(sc *ScoreCache) { c.cache = sc }

func (c *Client) backendKind() decisions.Backend {
	if c.backend != nil {
		return c.backend.Backend()
	}
	return decisions.BackendOpenRouter
}

// PreviewsAllowed says whether declaration names, which come from the user's
// files, may be put in the labels sent to this backend. pref is the user's
// jev.locate_previews setting: "off" never, "hosted" always, and otherwise
// (the default) only to a backend the user runs: the local decision model or
// a self-hosted server. OpenRouter and TypeSafe's hosted API get paths only.
func (c *Client) PreviewsAllowed(pref string) bool {
	switch pref {
	case config.LocatePreviewsOff:
		return false
	case config.LocatePreviewsHosted:
		return true
	}
	switch c.backendKind() {
	case decisions.BackendLocal:
		return true
	case decisions.BackendSystemOne:
		// Only a backend that says it is not hosted qualifies; one that
		// cannot say gets paths only.
		if h, ok := c.backend.(interface{ Hosted() bool }); ok {
			return !h.Hosted()
		}
	}
	return false
}

func (c *Client) limits() (items, bytes, workers int) {
	if c.Local() {
		return maxItemsLocal, maxBytesLocal, 1
	}
	return maxItemsRemote, maxBytesRemote, scoreWorkers
}

// Score rates every item against the criterion. It answers each item with a
// probability in [0,1] or marks it unknown; it never returns a made-up score.
//
// One request carries as many items as fit the backend's limits. A request
// that fails is halved and retried (except after HTTP 429, where retrying
// harder makes it worse); an item whose own request fails is unknown. A
// refused credential stops the call. The context bounds the whole call.
func (c *Client) Score(ctx context.Context, req ScoreRequest) (ScoreResult, error) {
	start := time.Now()
	res := ScoreResult{
		Scores:   make(map[string]float64, len(req.Items)),
		Identity: c.Identity(),
		Backend:  c.backendKind(),
	}
	seen := map[string]bool{}
	var todo []ScoreItem
	keys := map[string]string{}
	base := scoreKeyBase(req, res.Identity)
	for _, it := range req.Items {
		if it.ID == "" || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		if !req.NoCache {
			k := scoreKey(base, it)
			keys[it.ID] = k
			if s, ok := c.cache.get(k); ok {
				res.Scores[it.ID] = s
				res.CacheHits++
				continue
			}
		}
		todo = append(todo, it)
	}
	maxReq := req.MaxRequests
	if maxReq <= 0 {
		maxReq = defaultRequests
	}
	maxItems, maxBytes, workers := c.limits()
	batches := packBatches(todo, req, maxItems, maxBytes)

	var mu sync.Mutex
	var authErr error
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, b := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func(b []ScoreItem) {
			defer wg.Done()
			defer func() { <-sem }()
			c.runBatch(ctx, req, b, 0, &mu, &res, &authErr, maxReq, keys)
		}(b)
	}
	wg.Wait()

	for _, it := range req.Items {
		if it.ID == "" {
			continue
		}
		if _, ok := res.Scores[it.ID]; !ok {
			res.Unknown = append(res.Unknown, it.ID)
		}
	}
	res.Unknown = dedupe(res.Unknown)
	res.Latency = time.Since(start)
	if authErr != nil {
		return res, authErr
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// runBatch sends one batch, halving it on a recoverable failure.
func (c *Client) runBatch(ctx context.Context, req ScoreRequest, items []ScoreItem, depth int,
	mu *sync.Mutex, res *ScoreResult, authErr *error, maxReq int, keys map[string]string) {
	if len(items) == 0 || ctx.Err() != nil {
		return
	}
	mu.Lock()
	if *authErr != nil || res.Requests >= maxReq {
		mu.Unlock()
		return
	}
	res.Requests++
	mu.Unlock()

	dreq := buildScoreRequest(req, items)
	answers, err := c.decide(ctx, dreq)
	if err == nil {
		mu.Lock()
		for _, it := range items {
			if s, ok := validNoul(answers[questionID(it.ID)]); ok {
				res.Scores[it.ID] = s
				if k, ok := keys[it.ID]; ok {
					c.cache.put(k, s)
				}
			}
		}
		mu.Unlock()
		return
	}
	class, status, unavailable := classifyFailure(err)
	mu.Lock()
	if res.FailClass == "" {
		res.FailClass = class
	}
	if class == decisions.ClassAuth {
		*authErr = ErrScoreAuth
		mu.Unlock()
		return
	}
	mu.Unlock()
	if len(items) > 1 && depth < maxSplitDepth && status != http.StatusTooManyRequests && unavailable {
		mu.Lock()
		res.Splits++
		mu.Unlock()
		half := (len(items) + 1) / 2
		c.runBatch(ctx, req, items[:half], depth+1, mu, res, authErr, maxReq, keys)
		c.runBatch(ctx, req, items[half:], depth+1, mu, res, authErr, maxReq, keys)
	}
}

// classifyFailure reads a decision failure from either transport: the
// decisions package's own errors, or the OpenRouter SDK's DecisionsError.
func classifyFailure(err error) (class decisions.Class, status int, unavailable bool) {
	class = decisions.ClassOf(err)
	status = decisions.StatusOf(err)
	unavailable = decisions.IsUnavailable(err)
	var de *DecisionsError
	if errors.As(err, &de) {
		status = de.Status
		switch {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			class = decisions.ClassAuth
		case status == 0 || status == http.StatusTooManyRequests || status >= 500:
			class, unavailable = decisions.ClassUnavailable, true
		default:
			class = decisions.ClassSchema
		}
	}
	if class == "" {
		class = decisions.ClassUnavailable
		unavailable = true
	}
	return class, status, unavailable
}

// questionID is the request key of an item's question.
func questionID(id string) string { return "s:" + id }

// buildScoreRequest lays a batch out as one state and one short question per
// item. The long criterion appears once, in the state.
func buildScoreRequest(req ScoreRequest, items []ScoreItem) decisions.Request {
	labels := make(map[string]string, len(items))
	questions := make(map[string]decisions.Question, len(items))
	for _, it := range items {
		labels[it.ID] = it.Label.String()
		questions[questionID(it.ID)] = decisions.Noul(
			"Apply state.criterion to state.items[" + strconv.Quote(it.ID) + "] given state.context. Answer true if it holds.")
	}
	state := map[string]any{
		"criterion": req.Criterion.String(),
		"context":   req.Context.String(),
		"items":     labels,
	}
	for k, v := range req.Extra {
		state[k] = v.String()
	}
	return decisions.Request{State: state, Questions: questions}
}

// packBatches groups items so each request stays under the backend's item and
// byte limits. An item larger than the byte limit travels alone.
func packBatches(items []ScoreItem, req ScoreRequest, maxItems, maxBytes int) [][]ScoreItem {
	fixed := req.Criterion.Len() + req.Context.Len()
	for _, v := range req.Extra {
		fixed += v.Len()
	}
	var out [][]ScoreItem
	var cur []ScoreItem
	size := fixed
	for _, it := range items {
		cost := it.Label.Len() + len(it.ID) + itemOverhead
		if len(cur) > 0 && (len(cur) >= maxItems || size+cost > maxBytes) {
			out = append(out, cur)
			cur, size = nil, fixed
		}
		cur = append(cur, it)
		size += cost
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// validNoul reads a noul answer, refusing anything that is not a finite
// probability.
func validNoul(a decisions.Answer) (float64, bool) {
	if a.Type != decisions.TypeNoul || math.IsNaN(a.Noul) || math.IsInf(a.Noul, 0) || a.Noul < 0 || a.Noul > 1 {
		return 0, false
	}
	return a.Noul, true
}

func scoreKeyBase(req ScoreRequest, identity string) string {
	h := sha256.New()
	for _, s := range []string{req.Job, identity, req.Criterion.String(), req.Context.String()} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	keys := make([]string, 0, len(req.Extra))
	for k := range req.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(req.Extra[k].String()))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func scoreKey(base string, it ScoreItem) string {
	h := sha256.Sum256([]byte(base + "\x00" + it.ID + "\x00" + it.Label.String()))
	return hex.EncodeToString(h[:])
}
