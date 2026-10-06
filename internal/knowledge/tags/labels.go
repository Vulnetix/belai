package tags

import (
	"path"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/depwatch"
	"github.com/vulnetix/belai/internal/locate"
)

// maxLabels bounds the static labels of one document.
const maxLabels = 40

// labelText bounds how much text the structural checks read.
const labelText = 64 << 10

// extraLang names languages locate.Language does not.
var extraLang = map[string]string{
	".php": "PHP", ".swift": "Swift", ".scala": "Scala", ".lua": "Lua", ".dart": "Dart",
	".ex": "Elixir", ".exs": "Elixir", ".erl": "Erlang", ".hs": "Haskell", ".ml": "OCaml",
	".clj": "Clojure", ".r": "R", ".jl": "Julia", ".pl": "Perl", ".pm": "Perl", ".zig": "Zig",
	".nim": "Nim", ".cr": "Crystal", ".groovy": "Groovy", ".sql": "SQL", ".proto": "Protobuf",
	".graphql": "GraphQL", ".gql": "GraphQL", ".sol": "Solidity", ".vue": "Vue", ".svelte": "Svelte",
	".tf": "Terraform", ".hcl": "HCL", ".nix": "Nix", ".ps1": "PowerShell", ".bat": "Batch",
	".m": "Objective-C", ".mm": "Objective-C", ".v": "Verilog", ".vhd": "VHDL", ".asm": "Assembly",
	".s": "Assembly", ".cmake": "CMake", ".gradle": "Gradle", ".rst": "reStructuredText",
}

// extKind maps an extension to a document type when no name rule applies.
var extKind = map[string]string{
	".md": "doc", ".mdx": "doc", ".rst": "doc", ".txt": "doc", ".adoc": "doc", ".org": "doc",
	".json": "config", ".yaml": "config", ".yml": "config", ".toml": "config", ".ini": "config",
	".cfg": "config", ".conf": "config", ".env": "config", ".properties": "config", ".xml": "config",
	".csv": "data", ".tsv": "data", ".jsonl": "data", ".ndjson": "data", ".parquet": "data",
	".sh": "script", ".bash": "script", ".zsh": "script", ".fish": "script", ".ps1": "script", ".bat": "script",
	".tf": "iac", ".hcl": "iac", ".bicep": "iac", ".nix": "iac",
	".html": "source", ".css": "source", ".scss": "source", ".sql": "source", ".proto": "source",
	".graphql": "source", ".gql": "source",
}

// langOf names the language of a file, or "" when it has none.
func langOf(base string) string {
	if l := locate.Language(base); l != "" && l != "text" && l != "Markdown" && l != "JSON" && l != "YAML" && l != "TOML" {
		return l
	}
	ext := strings.ToLower(path.Ext(base))
	if l, ok := extraLang[ext]; ok {
		return l
	}
	switch strings.ToLower(base) {
	case "makefile", "gnumakefile":
		return "Make"
	case "justfile":
		return "Just"
	case "dockerfile":
		return "Dockerfile"
	}
	return ""
}

// slugLabel makes a label value: lower case, letters, digits and . _ + -.
func slugLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '+', r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '/':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// normLabel lowercases a label and cleans its value the way Labels does.
func normLabel(l string) string {
	k, v, ok := strings.Cut(l, ":")
	if !ok {
		return slugLabel(l)
	}
	return strings.ToLower(strings.TrimSpace(k)) + ":" + slugLabel(v)
}

// Labels derives a document's type, language and static labels from its path,
// size and structure. It uses no model and reads only the first labelText
// bytes of the chunk text.
func Labels(in Input) (kind, lang string, labels []string) {
	rel := strings.TrimPrefix(strings.ReplaceAll(in.Rel, "\\", "/"), "./")
	base := path.Base(rel)
	lower := strings.ToLower(base)
	ext := strings.ToLower(path.Ext(base))
	set := map[string]bool{}
	add := func(k, v string) {
		if v = slugLabel(v); v != "" && len(set) < maxLabels {
			set[k+":"+v] = true
		}
	}

	text := head(in.Chunks, labelText)
	lang = langOf(base)

	switch {
	case in.Artifact != "":
		kind = "scanner"
		add("kind", in.Artifact)
		add("tool", in.Tool)
	default:
		kind = nameKind(rel, lower, ext, lang)
		if info, ok := depwatch.Detect(rel, firstChunk(in.Chunks)); ok && kind != "ci" && kind != "test" {
			switch {
			case info.Type == "ci-pipeline" || info.Ecosystem == "ci" || strings.Contains(info.Type, "actions") || strings.Contains(info.Type, "pipeline"):
				kind = "ci"
			case strings.Contains(strings.ToLower(info.Type), "docker"), info.Ecosystem == "kubernetes", info.Ecosystem == "docker":
				if kind != "ci" {
					kind = "iac"
				}
			case info.Type == "shell-script":
				kind = "script"
			case info.Lock:
				kind = "dependency"
				add("kind", "lockfile")
			default:
				kind = "dependency"
				add("kind", "manifest")
			}
			add("eco", info.Ecosystem)
			add("manifest", info.Type)
		}
		if k := docKind(rel, lower); k != "" {
			add("kind", k)
		}
	}
	add("type", kind)
	if lang != "" {
		add("lang", lang)
	}
	if ext != "" {
		add("ext", strings.TrimPrefix(ext, "."))
	}
	if dir := firstDir(rel); dir != "" {
		add("dir", dir)
	}
	add("size", sizeBucket(in.Size))
	if in.Records {
		add("shape", "records")
	}
	for _, h := range structure(text, kind) {
		add("has", h)
	}
	for _, w := range headingWords(text, kind) {
		add("heading", w)
	}

	labels = make([]string, 0, len(set))
	for l := range set {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return kind, lang, labels
}

// nameKind classifies by name and extension.
func nameKind(rel, lower, ext, lang string) string {
	switch {
	case isTest(rel, lower):
		return "test"
	case lower == "makefile" || lower == "gnumakefile" || lower == "justfile" || lower == "rakefile" || lower == "cmakelists.txt" || strings.HasSuffix(lower, ".mk"):
		return "build"
	case strings.Contains(rel, ".github/workflows/") || lower == ".gitlab-ci.yml" || lower == "jenkinsfile" || strings.Contains(rel, ".circleci/"):
		return "ci"
	case strings.Contains(lower, "dockerfile") || strings.Contains(lower, "containerfile") || strings.HasSuffix(lower, ".tf"):
		return "iac"
	}
	if k, ok := extKind[ext]; ok {
		return k
	}
	if lang != "" {
		return "source"
	}
	return "text"
}

// isTest recognises a test file by the conventions of the common ecosystems.
func isTest(rel, lower string) bool {
	switch {
	case strings.HasSuffix(lower, "_test.go"), strings.HasSuffix(lower, "_test.py"), strings.HasSuffix(lower, "_spec.rb"),
		strings.HasPrefix(lower, "test_") && strings.HasSuffix(lower, ".py"),
		strings.Contains(lower, ".test."), strings.Contains(lower, ".spec."),
		strings.HasSuffix(lower, "test.java"), strings.HasSuffix(lower, "tests.cs"), strings.HasSuffix(lower, "test.kt"):
		return true
	}
	for _, seg := range strings.Split(strings.ToLower(path.Dir(rel)), "/") {
		switch seg {
		case "test", "tests", "__tests__", "spec", "specs", "e2e", "testdata":
			return true
		}
	}
	return false
}

// docKind names well-known documents.
func docKind(rel, lower string) string {
	stem := strings.TrimSuffix(lower, path.Ext(lower))
	switch stem {
	case "readme", "changelog", "license", "licence", "contributing", "security", "notice", "authors",
		"codeowners", "roadmap", "migration", "upgrading", "architecture", "design", "faq", "todo":
		return stem
	case "code_of_conduct", "code-of-conduct":
		return "code-of-conduct"
	}
	r := strings.ToLower(rel)
	switch {
	case strings.Contains(r, "/adr/"), strings.Contains(r, "/decisions/"), strings.HasPrefix(r, "adr/"):
		return "adr"
	case strings.Contains(r, "runbook"), strings.Contains(r, "playbook"):
		return "runbook"
	case strings.Contains(r, "/rfc"), strings.HasPrefix(r, "rfc"):
		return "rfc"
	case strings.Contains(r, "postmortem"), strings.Contains(r, "post-mortem"), strings.Contains(r, "incident"):
		return "incident"
	}
	return ""
}

func firstDir(rel string) string {
	i := strings.IndexByte(rel, '/')
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

func sizeBucket(n int64) string {
	switch {
	case n < 1<<10:
		return "tiny"
	case n < 16<<10:
		return "small"
	case n < 256<<10:
		return "medium"
	}
	return "large"
}

// head joins chunk text up to max bytes, cutting on a rune boundary.
func head(chunks []string, max int) string {
	var b strings.Builder
	for _, c := range chunks {
		if b.Len() >= max {
			break
		}
		room := max - b.Len()
		if len(c) > room {
			c = cut(c, room)
		}
		b.WriteString(c)
		b.WriteByte('\n')
	}
	return b.String()
}

// cut shortens s to at most n bytes without splitting a rune.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func firstChunk(chunks []string) string {
	if len(chunks) == 0 {
		return ""
	}
	return cut(chunks[0], 4<<10)
}

// structure names what the text contains, from cheap markers.
func structure(text, kind string) []string {
	if text == "" {
		return nil
	}
	var out []string
	if strings.HasPrefix(text, "---\n") || strings.HasPrefix(text, "+++\n") {
		out = append(out, "frontmatter")
	}
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		out = append(out, "code-fences")
	}
	if strings.Contains(text, "|---") || strings.Contains(text, "| ---") || strings.Contains(text, "|:--") {
		out = append(out, "tables")
	}
	if strings.Contains(text, "TODO") || strings.Contains(text, "FIXME") {
		out = append(out, "todo")
	}
	if strings.Contains(text, "func Test") || strings.Contains(text, "def test_") || strings.Contains(text, "describe(") || strings.Contains(text, "@Test") {
		out = append(out, "tests")
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		out = append(out, "links")
	}
	if kind == "doc" && strings.Contains(text, "\n# ") || strings.HasPrefix(text, "# ") {
		out = append(out, "headings")
	}
	return out
}

// headingWords returns up to six distinct words of at least four letters from
// the first markdown headings. They are the document's own words, reduced to
// label characters and capped, so a heading cannot carry markup or a sentence.
func headingWords(text, kind string) []string {
	if kind != "doc" || text == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	heads := 0
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heads++
		if heads > 3 {
			break
		}
		for _, w := range strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
			return !(r >= 'a' && r <= 'z')
		}) {
			if len(w) >= 4 && len(w) <= 24 && !seen[w] && !stopWord[w] {
				seen[w] = true
				out = append(out, w)
				if len(out) == 6 {
					return out
				}
			}
		}
	}
	return out
}

var stopWord = map[string]bool{
	"this": true, "that": true, "with": true, "from": true, "your": true, "have": true, "will": true,
	"into": true, "about": true, "what": true, "when": true, "which": true, "table": true, "contents": true,
}
