package agentimport

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Other harnesses write their commands, prompts, skills and agents as Markdown
// with optional YAML front matter. None of the conventions is a standard, so a
// harness is a small spec here: its name for the report, the file-name suffixes
// that are not part of an item's name, and the keys it is known to write. The
// keys every harness shares (name, description, argument-hint, allowed-tools)
// are read the same way everywhere; every other key is named in the report as
// not imported.

type hspec struct {
	label string
}

var hspecs = map[Format]hspec{
	ClaudeCode: {"Claude Code"},
	Cursor:     {"Cursor"},
	Codex:      {"OpenAI Codex"},
	GeminiCLI:  {"Gemini CLI"},
	OpenCode:   {"opencode"},
	Windsurf:   {"Windsurf"},
	Copilot:    {"GitHub Copilot"},
	Cline:      {"Cline"},
	GenericMD:  {"another harness"},
	Belai:      {"Belai"},
}

// HarnessFormats lists the formats of other harnesses' files, in the order the
// registry names them.
func HarnessFormats() []Format {
	return []Format{ClaudeCode, Cursor, Codex, GeminiCLI, OpenCode, Windsurf, Copilot, Cline, GenericMD}
}

func isHarness(f Format) bool {
	for _, h := range HarnessFormats() {
		if h == f {
			return true
		}
	}
	return false
}

// ParseItemFormat reads any format name ImportItem takes: the harness formats,
// "belai" and the four of Import.
func ParseItemFormat(s string) (Format, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, f := range append(append(HarnessFormats(), Belai), Formats()...) {
		if string(f) == s {
			return f, true
		}
	}
	return "", false
}

func (f Format) label() string {
	if s, ok := hspecs[f]; ok {
		return s.label
	}
	return string(f)
}

// nameSuffixes are the parts of a file name that say what the file is, not what
// the item is called: review.prompt.md is the prompt "review".
var nameSuffixes = []string{".prompt", ".agent", ".chatmode", ".instructions"}

// itemStem is the item's name as its file or folder says it.
func itemStem(base string) string {
	l := strings.ToLower(base)
	for _, s := range nameSuffixes {
		if strings.HasSuffix(l, s) && len(base) > len(s) {
			return base[:len(base)-len(s)]
		}
	}
	return base
}

// fmKey folds a front-matter key to one spelling: lower case, hyphens.
func fmKey(k string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "_", "-")
}

var legacyKV = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_.-]*):[ \t]*(.*)$`)

// softFrontMatter splits a Markdown file into its front matter and body. It
// insists on neither: a file with no front matter, or whose front matter is not
// closed, is all body. Front matter that is not valid YAML (a description such
// as "Cut a release: tag" is common) is read line by line.
func softFrontMatter(text string) (fm doc, body string, has bool) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, text, false
	}
	var raw, after string
	if r, ok := strings.CutPrefix(rest, "---"); ok {
		raw, after = "", r
	} else if idx := strings.Index(rest, "\n---"); idx >= 0 {
		raw, after = rest[:idx], rest[idx+len("\n---"):]
	} else {
		return nil, text, false
	}
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		after = after[nl+1:]
	} else {
		after = ""
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		m = map[string]any{}
		if err != nil {
			for _, l := range strings.Split(raw, "\n") {
				if g := legacyKV.FindStringSubmatch(l); g != nil {
					m[g[1]] = strings.Trim(strings.TrimSpace(g[2]), `"'`)
				}
			}
		}
	}
	return doc(m), strings.TrimSpace(after), true
}

// fmText writes a front-matter value as one line of text.
func fmText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		var out []string
		for _, e := range x {
			if s := fmText(e); s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, ", ")
	case map[string]any:
		var out []string
		for _, k := range doc(x).keys() {
			out = append(out, k+"="+fmText(x[k]))
		}
		return strings.Join(out, ", ")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// fmList reads a value that is a list or a comma-separated string.
func fmList(v any) []string {
	switch x := v.(type) {
	case string:
		var out []string
		for _, p := range strings.Split(x, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

var (
	claudeBang  = regexp.MustCompile("!`[^`\n]+`")
	geminiArgs  = regexp.MustCompile(`\{\{\s*args\s*\}\}`)
	dropReasons = map[string]string{
		"model":           "the item runs on the session's model, not a harness model name",
		"hooks":           "hooks are never taken from an item",
		"mcpservers":      "MCP servers are your own settings",
		"mcp-servers":     "MCP servers are your own settings",
		"permission":      "permissions are yours; Belai's own rules apply",
		"permissionmode":  "permissions are yours; Belai's own rules apply",
		"permission-mode": "permissions are yours; Belai's own rules apply",
	}
)

// bodyNotes names text in a body that means something to the source harness and
// is plain text in Belai.
func bodyNotes(n *noter, f Format, body string) {
	if claudeBang.MatchString(body) {
		n.add(Warning, "body", "it uses !`command` lines, which run in %s; Belai keeps them as plain text and never runs them", f.label())
	}
	if geminiArgs.MatchString(body) {
		n.add(Warning, "body", "it uses {{args}}, which is Gemini CLI's placeholder; Belai fills $ARGUMENTS, so it stays as written")
	}
}

// dropNote reports a front-matter key that has no place in the item.
func dropNote(n *noter, f Format, key string) {
	why, ok := dropReasons[strings.ReplaceAll(key, "-", "")]
	if !ok {
		why, ok = dropReasons[key]
	}
	if !ok {
		why = fmt.Sprintf("a %s key Belai has no field for", f.label())
	}
	n.add(Dropped, key, "not imported: %s", why)
}
