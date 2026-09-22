package lsp

import (
	"path/filepath"
	"strings"
)

// Language describes one supported language.
type Language struct {
	ID, Display string
	Exts        []string
	Basenames   []string
	Server      string
	Alts        []string
	Args        []string
	Install     []string
	Fallback    []string
	Notes       string
}

// languages is the canonical static registry. It is ordered by Display for
// stable UI rendering; order has no semantic effect.
var languages = []Language{
	{
		ID: "bash", Display: "Shell",
		Exts:   []string{".sh", ".bash"},
		Server: "bash-language-server", Alts: nil, Args: []string{"start"},
		Install:  []string{"npm", "install", "-g", "bash-language-server"},
		Fallback: []string{"bash", "-n", "%s"},
		Notes:    "",
	},
	{
		ID: "c", Display: "C",
		Exts:   []string{".c", ".h"},
		Server: "clangd", Alts: nil, Args: []string{"--background-index=false", "--log=error"},
		Install:  []string{"brew", "install", "llvm"},
		Fallback: []string{"clang", "-fsyntax-only", "-x", "c", "%s"},
		Notes:    "shares clangd with C++ / Objective-C",
	},
	{
		ID: "cpp", Display: "C++",
		Exts:   []string{".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"},
		Server: "clangd", Alts: nil, Args: []string{"--background-index=false", "--log=error"},
		Install:  []string{"brew", "install", "llvm"},
		Fallback: []string{"clang", "-fsyntax-only", "-x", "c++", "%s"},
		Notes:    "shares clangd with C / Objective-C",
	},
	{
		ID: "csharp", Display: "C#",
		Exts:   []string{".cs"},
		Server: "csharp-ls", Alts: []string{"omnisharp"}, Args: []string{"-lsp"},
		Install:  []string{"dotnet", "tool", "install", "-g", "csharp-ls"},
		Fallback: nil,
		Notes:    "",
	},
	{
		ID: "dart", Display: "Dart / Flutter",
		Exts:   []string{".dart"},
		Server: "dart", Alts: nil, Args: []string{"language-server", "--protocol=lsp"},
		Install:  nil,
		Fallback: []string{"dart", "analyze", "%s"},
		Notes:    "covers Flutter",
	},
	{
		ID: "go", Display: "Go",
		Exts:   []string{".go"},
		Server: "gopls", Alts: nil, Args: []string{"serve"},
		Install:  []string{"go", "install", "golang.org/x/tools/gopls@latest"},
		Fallback: []string{"gofmt", "-e", "%s"},
		Notes:    "",
	},
	{
		ID: "java", Display: "Java",
		Exts:   []string{".java"},
		Server: "jdtls", Alts: nil, Args: []string{"-data", "<cachedir>"},
		Install:  nil,
		Fallback: nil,
		Notes:    "",
	},
	{
		ID: "objc", Display: "Objective-C",
		Exts:   []string{".m", ".mm"},
		Server: "clangd", Alts: nil, Args: []string{"--background-index=false", "--log=error"},
		Install:  []string{"brew", "install", "llvm"},
		Fallback: []string{"clang", "-fsyntax-only", "-x", "objective-c", "%s"},
		Notes:    "shares clangd with C / C++",
	},
	{
		ID: "python", Display: "Python",
		Exts:   []string{".py", ".pyi"},
		Server: "pyright-langserver", Alts: []string{"ruff", "pylsp"}, Args: []string{"--stdio"},
		Install:  []string{"npm", "install", "-g", "pyright"},
		Fallback: []string{"python3", "-m", "py_compile", "%s"},
		Notes:    "",
	},
	{
		ID: "ruby", Display: "Ruby",
		Exts:      []string{".rb", ".rake", ".gemspec"},
		Basenames: []string{"Rakefile", "Gemfile"},
		Server:    "ruby-lsp", Alts: []string{"solargraph"}, Args: []string{"stdio"},
		Install:  []string{"gem", "install", "ruby-lsp"},
		Fallback: []string{"ruby", "-c", "%s"},
		Notes:    "",
	},
	{
		ID: "rust", Display: "Rust",
		Exts:   []string{".rs"},
		Server: "rust-analyzer", Alts: nil, Args: nil,
		Install:  []string{"rustup", "component", "add", "rust-analyzer"},
		Fallback: []string{"rustfmt", "--check", "--edition", "2021", "%s"},
		Notes:    "",
	},
	{
		ID: "swift", Display: "Swift",
		Exts:   []string{".swift"},
		Server: "sourcekit-lsp", Alts: nil, Args: nil,
		Install:  nil,
		Fallback: []string{"swiftc", "-parse", "%s"},
		Notes:    "",
	},
	{
		ID: "ts", Display: "TypeScript / JavaScript",
		Exts:   []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
		Server: "typescript-language-server", Alts: nil, Args: []string{"--stdio"},
		Install:  []string{"npm", "install", "-g", "typescript-language-server", "typescript"},
		Fallback: []string{"node", "--check", "%s"},
		Notes:    "covers React, React Native, Node",
	},
	{
		ID: "zig", Display: "Zig",
		Exts:   []string{".zig", ".zon"},
		Server: "zls", Alts: nil, Args: nil,
		Install:  []string{"brew", "install", "zls"},
		Fallback: []string{"zig", "ast-check", "%s"},
		Notes:    "",
	},
}

var (
	langByID   map[string]*Language
	extToLang  map[string]*Language
	nameToLang map[string]*Language
)

func init() {
	langByID = make(map[string]*Language, len(languages))
	extToLang = make(map[string]*Language)
	nameToLang = make(map[string]*Language)
	for i := range languages {
		l := &languages[i]
		langByID[l.ID] = l
		for _, e := range l.Exts {
			extToLang[e] = l
		}
		for _, n := range l.Basenames {
			nameToLang[n] = l
		}
	}
}

// Languages returns the full static registry in Display order.
func Languages() []Language {
	out := make([]Language, len(languages))
	copy(out, languages)
	return out
}

// LanguageIDs returns the canonical language IDs.
func LanguageIDs() []string {
	out := make([]string, 0, len(languages))
	for _, l := range languages {
		out = append(out, l.ID)
	}
	return out
}

// LanguageFor returns the language matching the path, or nil if unsupported.
func LanguageFor(path string) *Language {
	base := filepath.Base(path)
	if l := nameToLang[base]; l != nil {
		return l
	}
	ext := strings.ToLower(filepath.Ext(path))
	if l := extToLang[ext]; l != nil {
		return l
	}
	return nil
}

// KnownLanguageID reports whether id is a supported language ID.
func KnownLanguageID(id string) bool {
	return langByID[id] != nil
}
