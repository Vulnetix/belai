package knowledge

import "strings"

// Search ranks the index's chunks against query and returns the best limit
// (zero or less means all that score at least MinScore). It is what a Set does
// for one index, exported for a view that browses a single one.
func (ix *Index) Search(query string, limit int) []Hit { return ix.search(query, limit) }

// Document returns the manifest entry of the document at address and its text,
// joined from its chunks with the overlap between neighbouring chunks removed
// by their line spans. It is a view of what the index holds, not the file: a
// document the cap truncated, or a chunk the gate dropped, is missing from it.
// The label chunk is not part of it.
func (ix *Index) Document(address string) (Doc, string, bool) {
	if ix == nil {
		return Doc{}, "", false
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	di := ix.docIndex(address)
	if di < 0 {
		return Doc{}, "", false
	}
	var (
		b       strings.Builder
		prevEnd int32
		first   = true
	)
	for _, c := range ix.chunks {
		if int(c.Doc) != di || c.Label {
			continue
		}
		text := c.Text
		switch {
		case first:
		case c.Start < prevEnd || (c.Start == prevEnd && c.End > c.Start):
			// The chunk repeats the end of the one before: drop those lines.
			lines := strings.Split(text, "\n")
			drop := int(prevEnd-c.Start) + 1
			if drop > len(lines) {
				drop = len(lines)
			}
			text = strings.Join(lines[drop:], "\n")
			if text != "" {
				b.WriteByte('\n')
			}
		case c.Start == c.End && c.Start == prevEnd:
			// A long line split into pieces: they join without a break.
		default:
			b.WriteByte('\n')
		}
		b.WriteString(text)
		prevEnd, first = c.End, false
	}
	return ix.docs[di], b.String(), true
}
