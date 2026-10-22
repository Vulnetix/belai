package rolemanager

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// ContentClass is what kind of text a file holds, which decides how much of
// it the security classifier reads. See docs/architecture.md, "Tool-result
// trust".
type ContentClass int

const (
	// ClassOther is anything that is neither code nor prose: data and config
	// formats, unknown extensions, command output. It is classified whole.
	ClassOther ContentClass = iota
	// ClassCode is source code in a programming language. Only its comments,
	// docstrings and multi-line strings are classified: they are the part of
	// a code file written for a reader, which is where an instruction to a
	// model would hide.
	ClassCode
	// ClassDoc is prose: markdown, plain text and the like. It is classified
	// whole, and goes to a SystemOne decision backend when one is available.
	ClassDoc
)

func (c ContentClass) String() string {
	switch c {
	case ClassCode:
		return "code"
	case ClassDoc:
		return "doc"
	}
	return "other"
}

// codeExts are programming-language extensions. Markup (html, xml, svg) and
// data or config formats (json, yaml, toml, ini, csv, sql) are not here: their
// text is not code a compiler checks, so they are classified whole.
var codeExts = map[string]bool{
	".go": true, ".py": true, ".pyi": true, ".pyx": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".java": true, ".kt": true, ".kts": true, ".scala": true, ".groovy": true, ".gradle": true, ".clj": true, ".cljs": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".m": true, ".mm": true,
	".cs": true, ".fs": true, ".fsx": true, ".vb": true,
	".rs": true, ".zig": true, ".zon": true, ".nim": true, ".d": true, ".v": true, ".odin": true,
	".swift": true, ".dart": true,
	".rb": true, ".rake": true, ".gemspec": true, ".php": true, ".pl": true, ".pm": true, ".lua": true, ".r": true, ".jl": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".psm1": true, ".bat": true, ".cmd": true,
	".ex": true, ".exs": true, ".erl": true, ".hrl": true, ".hs": true, ".ml": true, ".mli": true, ".elm": true,
	".vue": true, ".svelte": true, ".astro": true,
	".sol": true, ".move": true, ".cairo": true,
	".tf": true, ".hcl": true, ".nix": true, ".proto": true, ".graphql": true, ".gql": true,
	".cmake": true, ".mk": true,
}

// codeNames are code files known by name rather than extension.
var codeNames = map[string]bool{
	"Makefile": true, "makefile": true, "GNUmakefile": true, "Dockerfile": true, "Containerfile": true,
	"Justfile": true, "justfile": true, "Rakefile": true, "Gemfile": true, "Vagrantfile": true, "BUILD": true, "WORKSPACE": true,
}

// docExts are prose formats.
var docExts = map[string]bool{
	".md": true, ".mdx": true, ".markdown": true, ".rst": true, ".adoc": true, ".asciidoc": true,
	".txt": true, ".text": true, ".org": true,
}

// docNames are prose files that usually carry no extension.
var docNames = map[string]bool{
	"README": true, "LICENSE": true, "LICENCE": true, "NOTICE": true, "AUTHORS": true, "CONTRIBUTORS": true,
	"CHANGELOG": true, "CHANGES": true, "COPYING": true, "AGENTS": true, "CLAUDE": true, "GEMINI": true,
}

// ClassOf returns the content class of a file path, from its name alone.
func ClassOf(path string) ContentClass {
	base := filepath.Base(path)
	if codeNames[base] {
		return ClassCode
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch {
	case codeExts[ext]:
		return ClassCode
	case docExts[ext]:
		return ClassDoc
	case ext == "" && docNames[strings.ToUpper(base)]:
		return ClassDoc
	}
	return ClassOther
}

// ExtractComments returns the comments, docstrings and multi-line string
// literals of a code file, each run prefixed with the line it starts on. It
// returns "" for code with none of them. A file no lexer recognises, or one
// the lexer fails on, comes back whole: when the comments cannot be told
// apart, everything is classified.
func ExtractComments(path, content string) string {
	if out, ok := fastComments(path, content); ok {
		return out
	}
	return chromaComments(path, content)
}

// chromaComments is ExtractComments through chroma's lexers: every language
// chroma knows, at about 1 MB/s.
func chromaComments(path, content string) string {
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		return content
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, content)
	if err != nil {
		return content
	}
	var out strings.Builder
	line := 1
	lastEnd := -1 // the line the previous comment run ended on
	for tok := it(); tok != chroma.EOF; tok = it() {
		if tok.Type == chroma.Error {
			return content
		}
		if isComment(tok) && strings.TrimSpace(tok.Value) != "" {
			if line != lastEnd {
				if out.Len() > 0 {
					out.WriteByte('\n')
				}
				out.WriteString(strconv.Itoa(line))
				out.WriteString(": ")
			} else {
				out.WriteByte(' ')
			}
			out.WriteString(strings.TrimSpace(tok.Value))
			lastEnd = line + strings.Count(tok.Value, "\n")
		}
		line += strings.Count(tok.Value, "\n")
	}
	return out.String()
}

// isComment reports whether a token is text written for a reader: a comment,
// a docstring, or any string literal that spans lines or is triple-quoted.
// Lexers rarely mark docstrings (Python's are plain strings), and a long
// string is where prose hides in code, so both count.
func isComment(t chroma.Token) bool {
	if t.Type.InCategory(chroma.Comment) || t.Type == chroma.LiteralStringDoc || t.Type == chroma.LiteralStringHeredoc {
		return true
	}
	if !t.Type.InSubCategory(chroma.LiteralString) {
		return false
	}
	v := strings.TrimLeft(t.Value, "rRbBuUfF")
	return strings.Contains(t.Value, "\n") || strings.HasPrefix(v, `"""`) || strings.HasPrefix(v, "'''")
}
