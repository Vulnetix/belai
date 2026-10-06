package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Gate decides whether one chunk of ingested text may be stored. In the
// harness it is the security classifier; it is never asked to answer from a
// search. admitted is false for a chunk the classifier flagged. An error fails
// closed: the document is not indexed.
type Gate func(ctx context.Context, text string) (admitted bool, err error)

// Doc is the manifest entry of one indexed document. Source is the host path
// the text came from; it filters hits against permission rules and is never
// shown to a model.
type Doc struct {
	Address string
	Source  string
	Size    int64
	// ModNano is the source's modification time in Unix nanoseconds, with
	// Size a cheap check that it has not changed since it was indexed.
	ModNano int64
	SHA     string
	Chunks  int
	Tokens  int
	// Dropped counts chunks the Gate did not admit.
	Dropped int
	// Truncated is true when the cap stopped the document short.
	Truncated bool
	// Tags is the document's type, labels and topics (package tags). A
	// document indexed before tags existed has the zero value and is tagged on
	// the next sync.
	Tags tags.Result
	// Artifact and Tool name the scanner artefact a .vulnetix file is, and
	// Records says each chunk is one scanner record; they feed the tagger.
	Artifact string
	Tool     string
	Records  bool
}

// storedChunk is one admitted chunk. Tf is its raw term-frequency vector; the
// weighted, normalised form is derived and never stored.
type storedChunk struct {
	Doc        int32
	Start, End int32
	Text       string
	Tokens     int32
	Tf         []feat
	// Label marks the one harness-composed chunk that carries the document's
	// labels so a search by label finds it. It is not part of the document's
	// text and holds nothing the tag tables do not name.
	Label bool
}

type posting struct {
	chunk int32
	w     float32
}

// Index holds one scope's admitted chunks. It is safe for concurrent use.
type Index struct {
	mu     sync.RWMutex
	name   string
	docs   []Doc
	chunks []storedChunk
	tokens int
	tagger tags.Tagger

	// derived by finalize
	dirty bool
	idf   map[uint32]float32
	post  map[uint32][]posting
}

// NewIndex returns an empty index. name labels it in hit rows ("project", a
// profile's name, "session").
func NewIndex(name string) *Index {
	return &Index{name: name, dirty: true}
}

// Name is the label the index was created with.
func (ix *Index) Name() string {
	if ix == nil {
		return ""
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.name
}

// Tokens is the estimated size of everything the index holds.
func (ix *Index) Tokens() int {
	if ix == nil {
		return 0
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.tokens
}

// Docs returns a copy of the manifest.
func (ix *Index) Docs() []Doc {
	if ix == nil {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]Doc(nil), ix.docs...)
}

// Input is one document, already cut into chunks, offered to Ingest.
type Input struct {
	Address string
	Source  string
	Size    int64
	ModTime time.Time
	// SHA is the SHA-256 of the source bytes; an unchanged document is not
	// re-gated.
	SHA    string
	Chunks []RawChunk
	// Artifact, Tool and Records describe a scanner artefact for the tagger.
	Artifact string
	Tool     string
	Records  bool
}

// Result reports what Ingest did with one document.
type Result struct {
	Admitted  int
	Dropped   int
	Reused    bool
	Truncated bool
	// Skipped is true when nothing of the document fit the cap.
	Skipped bool
}

// ErrCapReached is returned by Ingest when not a single chunk fit.
var ErrCapReached = errors.New("knowledge: token cap reached")

// Ingest stores a document, replacing any earlier one at the same address.
// capTokens bounds the whole index (0 or less means unbounded). A document
// whose SHA-256 matches the stored one is kept as is and never re-gated. A
// chunk is sanitised, then offered to gate (nil admits, which is the
// guardrails-off posture: sanitising alone). The first gate error aborts the
// document and leaves the index as it was.
func (ix *Index) Ingest(ctx context.Context, in Input, gate Gate, capTokens int) (Result, error) {
	// The index is read-locked only to look, and write-locked only to commit:
	// the gate may be a network round trip, and searches must not wait on it.
	// Ingest calls on one index must not run concurrently with each other.
	ix.mu.Lock()
	if old := ix.docIndex(in.Address); old >= 0 {
		d := ix.docs[old]
		if d.SHA != "" && d.SHA == in.SHA {
			ix.docs[old].Source, ix.docs[old].Size, ix.docs[old].ModNano = in.Source, in.Size, in.ModTime.UnixNano()
			ix.mu.Unlock()
			ix.refreshTags(ctx, in.Address)
			return Result{Admitted: d.Chunks, Dropped: d.Dropped, Reused: true, Truncated: d.Truncated}, nil
		}
	}
	// Admit into a scratch list first, so a gate error changes nothing.
	room := math.MaxInt
	if capTokens > 0 {
		room = capTokens - ix.tokensWithout(in.Address)
	}
	ix.mu.Unlock()
	var (
		cands []storedChunk
		res   Result
		plan  int
	)
	for _, c := range in.Chunks {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		text := sanitize.Text(c.Text)
		if text == "" {
			continue
		}
		tok := estimate(text)
		if plan+tok > room {
			res.Truncated = true
			break
		}
		cands = append(cands, storedChunk{Start: int32(c.Start), End: int32(c.End), Text: text, Tokens: int32(tok), Tf: termFreqs(text)})
		plan += tok
	}
	admitted, err := admit(ctx, gate, cands)
	if err != nil {
		return Result{}, fmt.Errorf("knowledge: gate %s: %w", in.Address, err)
	}
	var (
		kept    []storedChunk
		used    int
		dropped int
	)
	for i, c := range cands {
		if !admitted[i] {
			dropped++
			continue
		}
		kept = append(kept, c)
		used += int(c.Tokens)
	}
	if len(kept) == 0 && res.Truncated {
		return Result{Skipped: true, Truncated: true}, ErrCapReached
	}
	// Tag the admitted text. The tagger may ask a decision backend, so it runs
	// before the commit lock is taken.
	doc := Doc{
		Address: in.Address, Source: in.Source, Size: in.Size, ModNano: in.ModTime.UnixNano(), SHA: in.SHA,
		Chunks: len(kept), Tokens: used, Dropped: dropped, Truncated: res.Truncated,
		Artifact: in.Artifact, Tool: in.Tool, Records: in.Records,
	}
	texts := make([]string, len(kept))
	for i, c := range kept {
		texts[i] = c.Text
	}
	doc.Tags = ix.tagging().Tag(ctx, tagInput(doc, texts))
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(in.Address)
	di := int32(len(ix.docs))
	for i := range kept {
		kept[i].Doc = di
	}
	ix.docs = append(ix.docs, doc)
	ix.chunks = append(ix.chunks, kept...)
	ix.chunks = append(ix.chunks, labelChunk(doc.Tags, relOf(in.Address), di))
	ix.tokens += used
	ix.dirty = true
	res.Admitted, res.Dropped = len(kept), dropped
	return res, nil
}

// Remove drops a document.
func (ix *Index) Remove(address string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(address)
}

// Retain drops every document whose address is not in keep, so a file that has
// gone from its source leaves the index.
func (ix *Index) Retain(keep map[string]bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, d := range append([]Doc(nil), ix.docs...) {
		if !keep[d.Address] {
			ix.removeLocked(d.Address)
		}
	}
}

// Fit drops documents from the end until the index is within capTokens, for a
// cap that was lowered after the index was built.
func (ix *Index) Fit(capTokens int) {
	if ix == nil || capTokens <= 0 {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for ix.tokens > capTokens && len(ix.docs) > 0 {
		ix.removeLocked(ix.docs[len(ix.docs)-1].Address)
	}
}

func (ix *Index) docIndex(address string) int {
	for i, d := range ix.docs {
		if d.Address == address {
			return i
		}
	}
	return -1
}

func (ix *Index) tokensWithout(address string) int {
	if i := ix.docIndex(address); i >= 0 {
		return ix.tokens - ix.docs[i].Tokens
	}
	return ix.tokens
}

func (ix *Index) removeLocked(address string) {
	di := ix.docIndex(address)
	if di < 0 {
		return
	}
	ix.tokens -= ix.docs[di].Tokens
	ix.docs = append(ix.docs[:di], ix.docs[di+1:]...)
	kept := ix.chunks[:0]
	for _, c := range ix.chunks {
		switch {
		case int(c.Doc) == di:
			continue
		case int(c.Doc) > di:
			c.Doc--
		}
		kept = append(kept, c)
	}
	for i := len(kept); i < len(ix.chunks); i++ {
		ix.chunks[i] = storedChunk{}
	}
	ix.chunks = kept
	ix.dirty = true
}

// finalize derives the idf table and the inverted index. The caller holds the
// write lock.
func (ix *Index) finalize() {
	df := map[uint32]int{}
	for _, c := range ix.chunks {
		for _, f := range c.Tf {
			df[f.B]++
		}
	}
	n := float64(len(ix.chunks))
	ix.idf = make(map[uint32]float32, len(df))
	for b, d := range df {
		ix.idf[b] = float32(math.Log(1 + n/float64(d)))
	}
	ix.post = make(map[uint32][]posting, len(df))
	for ci, c := range ix.chunks {
		w := weigh(c.Tf, ix.idf)
		for _, f := range w {
			ix.post[f.B] = append(ix.post[f.B], posting{chunk: int32(ci), w: f.W})
		}
	}
	ix.dirty = false
}

// weigh applies idf to a term-frequency vector and scales it to unit length.
func weigh(tf []feat, idf map[uint32]float32) []feat {
	out := make([]feat, 0, len(tf))
	var sum float64
	for _, f := range tf {
		w := f.W * idf[f.B]
		if w == 0 {
			continue
		}
		out = append(out, feat{B: f.B, W: w})
		sum += float64(w) * float64(w)
	}
	if sum == 0 {
		return nil
	}
	norm := float32(1 / math.Sqrt(sum))
	for i := range out {
		out[i].W *= norm
	}
	return out
}

func (ix *Index) ready() {
	ix.mu.RLock()
	d := ix.dirty
	ix.mu.RUnlock()
	if !d {
		return
	}
	ix.mu.Lock()
	if ix.dirty {
		ix.finalize()
	}
	ix.mu.Unlock()
}

// Hit is one retrieved passage.
type Hit struct {
	// Index is the name of the index it came from.
	Index   string
	Address string
	// Source is the host path of the document; for permission checks only.
	Source     string
	Start, End int
	Text       string
	Tokens     int
	Score      float64
	// Label is true for the chunk that carries the document's labels.
	Label bool
	// Labels are the document's searchable labels (type, language, topics).
	Labels []string
}

// MinScore is the cosine similarity below which a chunk is not a hit. Hashed
// vectors collide a little, and a chance overlap should not surface.
const MinScore = 0.10

// search ranks the index's chunks against query and returns the best limit.
func (ix *Index) search(query string, limit int) []Hit {
	if ix == nil {
		return nil
	}
	ix.ready()
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if len(ix.chunks) == 0 {
		return nil
	}
	q := weigh(termFreqs(query), ix.idf)
	if len(q) == 0 {
		return nil
	}
	score := map[int32]float32{}
	for _, f := range q {
		for _, p := range ix.post[f.B] {
			score[p.chunk] += f.W * p.w
		}
	}
	type scored struct {
		c int32
		s float32
	}
	var all []scored
	for c, s := range score {
		if float64(s) >= MinScore {
			all = append(all, scored{c, s})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].c < all[j].c
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]Hit, 0, len(all))
	for _, a := range all {
		c := ix.chunks[a.c]
		d := ix.docs[c.Doc]
		out = append(out, Hit{
			Index: ix.name, Address: d.Address, Source: d.Source,
			Start: int(c.Start), End: int(c.End), Text: c.Text, Tokens: int(c.Tokens), Score: float64(a.s),
			Label: c.Label, Labels: d.Tags.All(),
		})
	}
	return out
}

func (ix *Index) addresses() []string {
	if ix == nil {
		return nil
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]string, 0, len(ix.docs))
	for _, d := range ix.docs {
		out = append(out, d.Address)
	}
	return out
}

// batchTokens is how much chunk text one gate call looks at. Scanner records
// are a dozen tokens each, so asking about every one alone would be thousands
// of classifier calls; a batch that is flagged is halved until the flagged
// chunk stands alone, and only that one is dropped.
const batchTokens = 1500

// admit returns, for each candidate, whether the gate admits it. A nil gate
// admits all (sanitising alone, the guardrails-off posture).
func admit(ctx context.Context, gate Gate, cands []storedChunk) ([]bool, error) {
	out := make([]bool, len(cands))
	if gate == nil {
		for i := range out {
			out[i] = true
		}
		return out, nil
	}
	for from := 0; from < len(cands); {
		to, tok := from, 0
		for to < len(cands) && (to == from || tok+int(cands[to].Tokens) <= batchTokens) {
			tok += int(cands[to].Tokens)
			to++
		}
		if err := admitRange(ctx, gate, cands, out, from, to); err != nil {
			return nil, err
		}
		from = to
	}
	return out, nil
}

func admitRange(ctx context.Context, gate Gate, cands []storedChunk, out []bool, from, to int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var b strings.Builder
	for i := from; i < to; i++ {
		if i > from {
			b.WriteString("\n\n")
		}
		b.WriteString(cands[i].Text)
	}
	ok, err := gate(ctx, b.String())
	if err != nil {
		return err
	}
	if ok {
		for i := from; i < to; i++ {
			out[i] = true
		}
		return nil
	}
	if to-from == 1 {
		return nil
	}
	mid := from + (to-from)/2
	if err := admitRange(ctx, gate, cands, out, from, mid); err != nil {
		return err
	}
	return admitRange(ctx, gate, cands, out, mid, to)
}
