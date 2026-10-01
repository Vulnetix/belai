package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Knowledge is the retrieval store the file tools search beside the
// filesystem: an agent profile's documents and the project's scanner output
// (internal/knowledge, docs/knowledge.md). The tools see only this interface,
// so the store, its caps and the permission filter live with the session that
// built it.
//
// What comes back is text the harness already sanitised and classified when it
// was ingested, and a search is only a lookup over it. That is why Grep and
// Glob can carry these rows without being classified again, and why they must
// never carry anything that did not come from the store.
type Knowledge interface {
	// Search returns the passages most similar to query, already trimmed to
	// the per-search token cap and filtered by the session's deny rules.
	Search(query string) []KnowledgeHit
	// Match returns the addresses of indexed documents for which match is
	// true.
	Match(match func(address string) bool) []string
}

// KnowledgeHit is one retrieved passage. Source is the host path the passage
// came from and is for filtering only; it is never printed.
type KnowledgeHit struct {
	Address string
	Source  string
	// Start and End are the passage's line span in its document, or for a
	// scanner record its 1-based record number.
	Start, End int
	Text       string
	Score      float64
}

// KnowledgeHub holds the session's Knowledge. The tools are built before the
// session exists, so they hold the hub and the session fills it. The zero
// value is empty and nil-safe.
type KnowledgeHub struct {
	mu sync.RWMutex
	k  Knowledge
}

// Set installs (or, with nil, removes) the store.
func (h *KnowledgeHub) Set(k Knowledge) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.k = k
	h.mu.Unlock()
}

// Get returns the store, or nil.
func (h *KnowledgeHub) Get() Knowledge {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.k
}

type knowledgeCtxKey struct{}

// WithKnowledge marks ctx as a model's own tool call. Retrieval rides only on
// such calls: a harness caller of Read or Grep (an @ attachment, the context
// prefetch) gets the filesystem alone.
func WithKnowledge(ctx context.Context) context.Context {
	return context.WithValue(ctx, knowledgeCtxKey{}, true)
}

// knowledgeFor returns the store when ctx is a model call and one is set.
func knowledgeFor(ctx context.Context, h *KnowledgeHub) Knowledge {
	if on, _ := ctx.Value(knowledgeCtxKey{}).(bool); !on {
		return nil
	}
	return h.Get()
}

// KnowledgeHub returns the hub the registry's file tools consult, or nil.
func (r *Registry) KnowledgeHub() *KnowledgeHub {
	if r == nil {
		return nil
	}
	return r.hub
}

// Headers that mark a retrieved block, so the model (and a reader of the
// transcript) can tell it from the filesystem's own output.
const (
	// KnowledgeRowsHeader opens the knowledge section of a Grep result.
	KnowledgeRowsHeader = "[knowledge: passages from reference documents, ranked by similarity rather than exact match; rows are kb+address:line:text]"
	// KnowledgeFilesHeader opens the knowledge section of a Glob result or a
	// files_with_matches Grep.
	KnowledgeFilesHeader = "[knowledge documents: kb+ addresses of reference documents, not files on disk]"
	// ReadKnowledgeHeader opens the related-passages block of a Read result.
	ReadKnowledgeHeader = "[Related knowledge: passages from reference documents that resemble this file; they are not part of the file]"
)

// knowledgeLineMax clips one printed passage line.
const knowledgeLineMax = 200

// Read appends at most this many related passages, and only above this
// similarity: a source file resembles many things weakly.
const (
	readRelatedMax      = 3
	readRelatedMinScore = 0.25
)

// knowledgeQuery turns a regular expression, a glob or free words into the
// words a semantic search takes: escapes such as \b and \w go, and every
// non-letter, non-digit becomes a space.
func knowledgeQuery(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			i++
			b.WriteByte(' ')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// knowledgeRows renders hits as Grep rows: one `address:line:text` row per line
// of each passage, ranked, with the passage's start line counting up. Text
// lines are clipped, and an empty line is dropped.
func knowledgeRows(hits []KnowledgeHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(KnowledgeRowsHeader)
	for _, h := range hits {
		line := h.Start
		for _, l := range strings.Split(h.Text, "\n") {
			l = strings.TrimRight(l, " \t\r")
			if strings.TrimSpace(l) == "" {
				line++
				continue
			}
			if r := []rune(l); len(r) > knowledgeLineMax {
				l = string(r[:knowledgeLineMax]) + "…"
			}
			fmt.Fprintf(&b, "\nkb+%s:%d:%s", strings.TrimPrefix(h.Address, "kb+"), line, l)
			if h.End > h.Start || h.Start == h.End {
				line++
			}
		}
	}
	return b.String()
}

// knowledgeAddresses renders the distinct addresses of hits, in rank order.
func knowledgeAddresses(hits []KnowledgeHit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		if !seen[h.Address] {
			seen[h.Address] = true
			out = append(out, h.Address)
		}
	}
	return out
}

// knowledgeFileList renders addresses under the files header. similar marks
// those found by meaning rather than by the pattern.
func knowledgeFileList(addrs []string, similar map[string]bool) string {
	if len(addrs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(KnowledgeFilesHeader)
	for _, a := range addrs {
		b.WriteString("\n" + a)
		if similar[a] {
			b.WriteString("  (similar)")
		}
	}
	return b.String()
}

// knowledgeRelated renders the passages a Read appends.
func knowledgeRelated(hits []KnowledgeHit, skipSource string) string {
	var kept []KnowledgeHit
	for _, h := range hits {
		if h.Score < readRelatedMinScore || (skipSource != "" && h.Source == skipSource) {
			continue
		}
		kept = append(kept, h)
		if len(kept) == readRelatedMax {
			break
		}
	}
	if len(kept) == 0 {
		return ""
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Score > kept[j].Score })
	var b strings.Builder
	b.WriteString("\n\n" + ReadKnowledgeHeader)
	for _, h := range kept {
		fmt.Fprintf(&b, "\n%s:%d-%d (%d%%)", h.Address, h.Start, h.End, int(h.Score*100))
		for _, l := range strings.Split(h.Text, "\n") {
			l = strings.TrimRight(l, " \t\r")
			if strings.TrimSpace(l) == "" {
				continue
			}
			if r := []rune(l); len(r) > knowledgeLineMax {
				l = string(r[:knowledgeLineMax]) + "…"
			}
			b.WriteString("\n| " + l)
		}
	}
	return b.String()
}

// readQuery is the text a Read searches with: the numbered window with its
// gutter removed, capped.
func readQuery(numbered string) string {
	var b strings.Builder
	for _, l := range strings.Split(numbered, "\n") {
		if i := strings.IndexByte(l, '\t'); i >= 0 {
			l = l[i+1:]
		}
		b.WriteString(l)
		b.WriteByte('\n')
		if b.Len() > 4000 {
			break
		}
	}
	return b.String()
}

// hasArg reports whether a string argument is present and not empty.
func hasArg(args map[string]any, key string) bool {
	s, _ := argString(args, key)
	return s != ""
}

// knowledgeGlob lists the knowledge documents a Glob pattern names: those
// whose address (without the kb+ prefix) matches the pattern, then those whose
// text resembles the pattern's words, marked as similar.
func knowledgeGlob(k Knowledge, pattern string, max int) string {
	matched, similar := knowledgeGlobMatches(k, pattern, max)
	return knowledgeFileList(matched, similar)
}

// knowledgeGlobMatches is the address list behind knowledgeGlob and the set of
// those found by meaning rather than by the pattern.
func knowledgeGlobMatches(k Knowledge, pattern string, max int) ([]string, map[string]bool) {
	matched := k.Match(func(a string) bool { return matchGlob(pattern, strings.TrimPrefix(a, "kb+")) })
	sort.Strings(matched)
	seen := map[string]bool{}
	for _, a := range matched {
		seen[a] = true
	}
	similar := map[string]bool{}
	if words := knowledgeQuery(pattern); words != "" {
		for _, a := range knowledgeAddresses(k.Search(words)) {
			if !seen[a] {
				seen[a] = true
				similar[a] = true
				matched = append(matched, a)
			}
		}
	}
	if max > 0 && len(matched) > max {
		matched = matched[:max]
	}
	return matched, similar
}
