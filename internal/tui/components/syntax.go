package components

import (
	"crypto/sha256"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Syntax highlighting exists for one case: an expanded panel, where the whole
// file or the whole diff is on screen and structure is what makes it readable.
// A collapsed row is three lines and gets none — there, the diff colours carry
// the meaning and a second colour system would only compete with them.

const (
	// syntaxMaxBytes skips highlighting for inputs large enough that lexing
	// would be felt. A file this size is not being read, it is being searched.
	syntaxMaxBytes = 512 * 1024
	// syntaxMaxLineCells skips minified bundles, where one "line" is the whole
	// file and the lexer does a great deal of work for a row nobody can read.
	syntaxMaxLineCells = 2000
	// syntaxCacheEntries bounds the highlight cache. A diff touches at most a
	// handful of files, so this only has to span one screenful of work.
	syntaxCacheEntries = 32
)

// Highlighted returns one slice of segments per line of src, or nil when the
// file type is unknown or highlighting is switched off. Callers render the
// plain text unchanged when it returns nil.
func Highlighted(path, src string) [][]Seg {
	if !syntaxEnabled() || len(src) > syntaxMaxBytes {
		return nil
	}
	lexer := lexerFor(path)
	if lexer == nil {
		return nil
	}
	return highlightedWith(lexer, src)
}

// HighlightedLang is Highlighted keyed by a language name instead of a
// filename. Fenced code blocks carry a language, not a path, so the two
// callers share the lexing, cache, size guards and colour mapping but resolve
// the lexer differently. Unknown languages still return nil: the no-content-
// analysis rule applies unchanged.
func HighlightedLang(lang, src string) [][]Seg {
	if !syntaxEnabled() || len(src) > syntaxMaxBytes || lang == "" {
		return nil
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return nil
	}
	return highlightedWith(chroma.Coalesce(lexer), src)
}

// highlightedWith tokenises src with the given lexer, caching the result by
// content hash so an entry can never be stale.
func highlightedWith(lexer chroma.Lexer, src string) [][]Seg {
	key := syntaxKey{lexer: lexer.Config().Name, hash: sha256.Sum256([]byte(src))}
	if out, ok := syntaxCacheGet(key); ok {
		return out
	}
	out := highlight(lexer, src)
	syntaxCachePut(key, out)
	return out
}

func syntaxEnabled() bool { return lipgloss.ColorProfile() != termenv.Ascii }

// lexerFor picks a lexer by filename only.
//
// Content analysis is deliberately not used. Chroma's Analyse, like the
// heuristics in other highlighters, guesses confidently and wrongly on prose
// and on short files — a changelog becomes a diff, a README becomes something
// stranger — and a mis-lexed file is worse than an unlexed one, because the
// colours then assert a structure that is not there.
func lexerFor(path string) chroma.Lexer {
	if path == "" {
		return nil
	}
	l := lexers.Match(filepath.Base(path))
	if l == nil {
		return nil
	}
	// Coalesce merges adjacent tokens of the same type, which means fewer
	// segments and fewer colour transitions per line.
	return chroma.Coalesce(l)
}

// highlight tokenises the whole input at once and splits the result into
// lines. Lexing per line would be cheaper to cache but wrong: a block comment,
// a raw string literal or a heredoc spans lines, and a per-line lexer restarts
// in the wrong state on every one of them.
func highlight(lexer chroma.Lexer, src string) [][]Seg {
	it, err := lexer.Tokenise(nil, src)
	if err != nil {
		return nil
	}

	lines := [][]Seg{{}}
	cur := 0
	for _, tok := range it.Tokens() {
		fg := syntaxColour(tok.Type)
		parts := strings.Split(tok.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				lines = append(lines, []Seg{})
				cur++
			}
			if part == "" {
				continue
			}
			if len(part) > syntaxMaxLineCells {
				return nil
			}
			lines[cur] = append(lines[cur], NewSeg(part, fg))
		}
	}
	return lines
}

// syntaxColour maps a chroma token to the existing palette.
//
// The mapping is deliberately coarse and leaves plain identifiers uncoloured.
// Belai has a six-colour brand palette, not a syntax theme; colouring every
// token class would both exhaust it and, by making everything significant,
// make nothing significant. What is coloured here is structure — the
// language's own words, the names being declared, and the literal data — which
// is what you scan for when reading a diff.
func syntaxColour(t chroma.TokenType) lipgloss.TerminalColor {
	switch t {
	case chroma.Error, chroma.GenericError, chroma.GenericTraceback:
		return ColorDanger
	}

	switch t.Category() {
	case chroma.Keyword:
		return ColorTeal
	case chroma.Comment:
		return ColorMuted
	case chroma.Literal:
		// Strings and numbers are content rather than structure, which is the
		// same role amber plays elsewhere in the UI.
		return ColorAmber
	case chroma.Operator:
		if t == chroma.OperatorWord {
			return ColorTeal
		}
		return ColorLine
	case chroma.Punctuation:
		return ColorLine
	case chroma.Name:
		switch t.SubCategory() {
		case chroma.NameFunction, chroma.NameClass, chroma.NameBuiltin,
			chroma.NameDecorator, chroma.NameNamespace, chroma.NameTag,
			chroma.NameException:
			return ColorTealSoft
		case chroma.NameAttribute:
			return ColorAmber
		}
		return nil
	}
	return nil
}

type syntaxKey struct {
	lexer string
	hash  [32]byte
}

// The cache is keyed by content hash, so an entry can never be stale and there
// is nothing to invalidate. It is guarded because rendering is single-goroutine
// today but background agents and tests are not, and the lock is uncontended.
var (
	syntaxMu    sync.Mutex
	syntaxCache = map[syntaxKey][][]Seg{}
	syntaxOrder []syntaxKey
)

func syntaxCacheGet(k syntaxKey) ([][]Seg, bool) {
	syntaxMu.Lock()
	defer syntaxMu.Unlock()
	out, ok := syntaxCache[k]
	return out, ok
}

func syntaxCachePut(k syntaxKey, v [][]Seg) {
	syntaxMu.Lock()
	defer syntaxMu.Unlock()
	if _, exists := syntaxCache[k]; !exists {
		syntaxOrder = append(syntaxOrder, k)
	}
	syntaxCache[k] = v
	for len(syntaxOrder) > syntaxCacheEntries {
		delete(syntaxCache, syntaxOrder[0])
		syntaxOrder = syntaxOrder[1:]
	}
}

// ResetSyntaxCache drops every cached highlight. Used when the transcript is
// cleared.
func ResetSyntaxCache() {
	syntaxMu.Lock()
	defer syntaxMu.Unlock()
	syntaxCache = map[syntaxKey][][]Seg{}
	syntaxOrder = nil
}
