// Package offload keeps oversized tool results out of the conversation.
//
// A tool result that has already been sanitised and admitted (classified
// SAFE, or shaped) and is larger than a threshold is kept here in full, and
// the conversation carries a head-and-tail preview with a reference instead.
// The model reads the rest on demand with the ReadResult tool, by reference,
// line window or pattern.
//
// Only admitted content is ever stored: a withheld result is never offered to
// Put. The store holds the session's results in memory only, bounded by
// MaxStoreBytes; nothing is written to disk, so no untrusted bytes land in the
// state directory. A reference that has been evicted (or that belongs to
// another session) answers "not available", never another result.
//
// The preview is a pure function of the content and the limits, cut on whole
// lines, and it replaces the result once, when the result is first added, so
// the conversation prefix stays byte-identical and prompt-cacheable.
package offload

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// CharsPerToken is the estimate used throughout: ~4 characters per token.
const CharsPerToken = 4

// MaxStoreBytes bounds the content a session keeps. When a new result would
// pass it, the oldest results are dropped first.
const MaxStoreBytes = 64 << 20

// maxPutBytes bounds one stored result; beyond it the tail is kept whole and
// the head is cut, since a failure summary lives at the end.
const maxPutBytes = 8 << 20

// headShare is the share of the preview given to the head; the rest is tail.
const headShare = 0.6

// Store is one session's offloaded results. It is safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	next  int
	size  int
	order []string
	items map[string]string
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{items: map[string]string{}}
}

// Tokens estimates the token size of s.
func Tokens(s string) int {
	return (utf8.RuneCountInString(s) + CharsPerToken - 1) / CharsPerToken
}

// Offload stores content and returns its preview when content is larger than
// thresholdTokens. ok is false (and content should be used as is) when it is
// not larger, or when previewing would not shrink it. toolName labels the
// reference in the preview's trailer; it is a harness identifier.
func (s *Store) Offload(toolName, content string, thresholdTokens, previewTokens int) (preview string, ok bool) {
	if s == nil || Tokens(content) <= thresholdTokens {
		return content, false
	}
	head, tail, marker := cut(content, previewTokens*CharsPerToken)
	if marker == "" {
		return content, false
	}
	ref := s.put(content)
	lines := strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
	var b strings.Builder
	b.WriteString(head)
	if head != "" && !strings.HasSuffix(head, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(marker)
	b.WriteByte('\n')
	b.WriteString(tail)
	if tail != "" && !strings.HasSuffix(tail, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[Offloaded %s output: ~%d tokens, %d lines. Only the head and tail are shown. "+
		"Read the rest with ReadResult(ref=%q) and offset/limit (1-based lines) or pattern (RE2).]",
		toolName, Tokens(content), lines, ref)
	return b.String(), true
}

// put stores content and returns its reference.
func (s *Store) put(content string) string {
	if len(content) > maxPutBytes {
		content = content[len(content)-maxPutBytes:]
		if i := strings.IndexByte(content, '\n'); i >= 0 {
			content = content[i+1:]
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	ref := fmt.Sprintf("r%d", s.next)
	for s.size+len(content) > MaxStoreBytes && len(s.order) > 0 {
		old := s.order[0]
		s.order = s.order[1:]
		s.size -= len(s.items[old])
		delete(s.items, old)
	}
	s.items[ref] = content
	s.order = append(s.order, ref)
	s.size += len(content)
	return ref
}

// Get returns the stored content for ref.
func (s *Store) Get(ref string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.items[ref]
	return c, ok
}

// cut splits content into a head and a tail of whole lines whose combined size
// fits budget characters, and returns the marker that stands for what lies
// between them ("" when nothing does). When no whole line fits at either end
// the first and last lines are cut inside, and the marker counts characters.
func cut(content string, budget int) (head, tail, marker string) {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	headBudget := int(float64(budget) * headShare)
	tailBudget := budget - headBudget
	h := 0
	used := 0
	for h < len(lines) && used+len(lines[h]) <= headBudget {
		used += len(lines[h])
		h++
	}
	t := len(lines)
	used = 0
	for t > h && used+len(lines[t-1]) <= tailBudget {
		used += len(lines[t-1])
		t--
	}
	if h == 0 && t == len(lines) {
		// No whole line fits: keep the start of the first line and the end of
		// the last, which may be the same line.
		head = truncRunes(lines[0], headBudget)
		tail = lastRunes(lines[len(lines)-1], tailBudget)
		elided := len(content) - len(head) - len(tail)
		if elided <= 0 {
			return "", "", ""
		}
		return head, tail, fmt.Sprintf("[… %d characters elided …]", elided)
	}
	if t == h {
		return "", "", ""
	}
	return strings.Join(lines[:h], ""), strings.Join(lines[t:], ""), fmt.Sprintf("[… %d lines elided …]", t-h)
}

func truncRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func lastRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// Slice selects part of stored content: the lines matching re with context
// lines around each match when re is non-nil, otherwise the window of limit
// lines starting at 1-based offset. The result is capped at maxChars and
// numbered like Read (`N\tline`), with a trailer naming what is left.
func Slice(content string, re *regexp.Regexp, offset, limit, context, maxChars int) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	var b strings.Builder
	if re != nil {
		keep := make([]bool, len(lines))
		matches := 0
		for i, l := range lines {
			if re.MatchString(l) {
				matches++
				for j := max(0, i-context); j <= min(len(lines)-1, i+context); j++ {
					keep[j] = true
				}
			}
		}
		if matches == 0 {
			return fmt.Sprintf("[no lines match; the result has %d lines]", len(lines))
		}
		prev := -2
		for i, k := range keep {
			if !k {
				continue
			}
			if prev >= 0 && i != prev+1 {
				b.WriteString("--\n")
			}
			row := sliceRow(i+1, lines[i])
			if b.Len()+len(row) > maxChars {
				fmt.Fprintf(&b, "[capped: narrow the pattern or use offset/limit; %d matching lines in all]", matches)
				return b.String()
			}
			b.WriteString(row)
			prev = i
		}
		fmt.Fprintf(&b, "[%d matching lines of %d]", matches, len(lines))
		return b.String()
	}
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return fmt.Sprintf("[offset %d is past the end: the result has %d lines]", offset, len(lines))
	}
	end := len(lines)
	if limit > 0 {
		end = min(end, offset-1+limit)
	}
	last := offset - 1
	for i := offset - 1; i < end; i++ {
		row := sliceRow(i+1, lines[i])
		if b.Len()+len(row) > maxChars && i > offset-1 {
			break
		}
		b.WriteString(row)
		last = i + 1
	}
	if last < len(lines) {
		fmt.Fprintf(&b, "[lines %d–%d of %d; continue with offset=%d]", offset, last, len(lines), last+1)
	} else {
		fmt.Fprintf(&b, "[lines %d–%d of %d; end of result]", offset, last, len(lines))
	}
	return b.String()
}

// maxSliceLine clips one line of a slice, as Read clips long lines, so a
// result that is one enormous line cannot flood the context through Slice.
const maxSliceLine = 2000

// sliceRow numbers one line like Read, clipping it at maxSliceLine.
func sliceRow(n int, line string) string {
	if len(line) > maxSliceLine {
		line = truncRunes(line, maxSliceLine) + "… [line clipped]"
	}
	return fmt.Sprintf("%d\t%s\n", n, line)
}
