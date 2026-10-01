package knowledge

import (
	"context"
	"strings"

	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/sanitize"
)

// SetTagger installs the tagger that labels documents as they are ingested.
// Without one the deterministic tags.Fast runs, so every document is tagged.
func (ix *Index) SetTagger(t tags.Tagger) {
	if ix == nil {
		return
	}
	ix.mu.Lock()
	ix.tagger = t
	ix.mu.Unlock()
}

func (ix *Index) tagging() tags.Tagger {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.tagger == nil {
		return tags.Fast{}
	}
	return ix.tagger
}

// relOf is the path inside a document's address: kb+scope/rel gives rel.
func relOf(address string) string {
	a := strings.TrimPrefix(address, addrPrefix)
	if i := strings.IndexByte(a, '/'); i >= 0 {
		return a[i+1:]
	}
	return a
}

func tagInput(d Doc, texts []string) tags.Input {
	return tags.Input{
		Rel: relOf(d.Address), Size: d.Size, Artifact: d.Artifact, Tool: d.Tool,
		Records: d.Records, Chunks: texts,
	}
}

// labelChunk is the one chunk, per document, that holds its labels as text.
// It is composed by the harness from the tag tables, then sanitised like any
// other chunk, and is never offered to a gate because no document text is in
// it. Its line is 0, which no real chunk has.
func labelChunk(r tags.Result, rel string, doc int32) storedChunk {
	text := sanitize.Text(tags.LabelText(r, rel))
	return storedChunk{Doc: doc, Text: text, Tokens: int32(estimate(text)), Tf: termFreqs(text), Label: true}
}

// textsLocked returns the document's own chunk texts in order. The caller
// holds the lock.
func (ix *Index) textsLocked(di int) []string {
	var out []string
	for _, c := range ix.chunks {
		if int(c.Doc) == di && !c.Label {
			out = append(out, c.Text)
		}
	}
	return out
}

// refreshTags tags a document that is already stored when its tags are stale
// (an older tag format, or a better tagger is now available), from the chunks
// it holds: the file is not read again and nothing is re-admitted.
func (ix *Index) refreshTags(ctx context.Context, address string) {
	tg := ix.tagging()
	ix.mu.RLock()
	di := ix.docIndex(address)
	if di < 0 || !tg.Refresh(ix.docs[di].Tags) {
		ix.mu.RUnlock()
		return
	}
	d := ix.docs[di]
	texts := ix.textsLocked(di)
	ix.mu.RUnlock()
	res := tg.Tag(ctx, tagInput(d, texts))
	ix.mu.Lock()
	defer ix.mu.Unlock()
	di = ix.docIndex(address)
	if di < 0 || ix.docs[di].SHA != d.SHA {
		return
	}
	ix.docs[di].Tags = res
	kept := ix.chunks[:0]
	for _, c := range ix.chunks {
		if int(c.Doc) == di && c.Label {
			continue
		}
		kept = append(kept, c)
	}
	for i := len(kept); i < len(ix.chunks); i++ {
		ix.chunks[i] = storedChunk{}
	}
	ix.chunks = append(kept, labelChunk(res, relOf(address), int32(di)))
	ix.dirty = true
}
