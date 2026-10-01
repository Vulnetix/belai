package tools

import (
	"fmt"
	"strings"
)

// Previews let a user's view show exactly what a model's own tool call would
// get from the knowledge store. Each one runs the same helper the tool runs, so
// the rows, the headers, the clipping and the similarity cut-offs cannot drift
// from what Grep, Glob and Read return. They are for a screen the user looks
// at; no model-facing tool uses them.

// PreviewGlobMax is the most knowledge addresses a Glob lists, the same cap a
// Glob call has by default.
const PreviewGlobMax = 200

// PreviewGrep is the knowledge section an unscoped Grep appends for pattern: in
// content mode the `kb+address:line:text` rows, with filesOnly the document
// list. hits are the passages behind it, ranked.
func PreviewGrep(k Knowledge, pattern string, filesOnly bool) (out string, hits []KnowledgeHit) {
	if k == nil {
		return "", nil
	}
	words := knowledgeQuery(pattern)
	if words == "" {
		return "", nil
	}
	hits = k.Search(words)
	if len(hits) == 0 {
		return "", nil
	}
	if filesOnly {
		return knowledgeFileList(knowledgeAddresses(hits), nil), hits
	}
	return knowledgeRows(hits), hits
}

// PreviewGlob is the knowledge section a Glob appends for pattern: the
// documents whose address matches it, then those that resemble its words,
// marked similar. addrs lists them in the order shown.
func PreviewGlob(k Knowledge, pattern string) (out string, addrs []string) {
	if k == nil {
		return "", nil
	}
	addrs, similar := knowledgeGlobMatches(k, pattern, PreviewGlobMax)
	return knowledgeFileList(addrs, similar), addrs
}

// PreviewRead is the related-passages block a Read of text appends: the
// passages that resemble it, above the similarity floor, at most three. text is
// shown numbered the way Read numbers it, and skipSource is the host path of
// the document itself, whose own indexed copy is left out.
func PreviewRead(k Knowledge, text, skipSource string) (out string, hits []KnowledgeHit) {
	if k == nil {
		return "", nil
	}
	var numbered strings.Builder
	for i, l := range strings.Split(text, "\n") {
		fmt.Fprintf(&numbered, "%d\t%s\n", i+1, l)
	}
	hits = k.Search(readQuery(numbered.String()))
	return strings.TrimLeft(knowledgeRelated(hits, skipSource), "\n"), hits
}
