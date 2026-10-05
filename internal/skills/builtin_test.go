package skills

import (
	"regexp"
	"strings"
	"testing"
)

var builtinNames = []string{"belai-builder", "belai-patcher", "belai-reviewer", "belai-scout", "belai-verifier", "belai-vuln-scout"}

// MaxBuiltinBodyLines is the room a builtin skill's text has: the contents list
// is part of it. A skill that needs more is carrying detail that belongs in the
// documentation it links to.
const maxBuiltinBodyLines = 100

// bannedNames are other agent products. A builtin skill states a standard of its
// own and names no product it was compared with.
var bannedNames = []string{
	"openhands", "swe-agent", "devin", "coderabbit", "copilot", "cursor", "aider",
	"claude", "codex", "qodo", "greptile", "hermes", "openclaw", "nemoclaw", "gemini",
}

var (
	tocLineRE  = regexp.MustCompile(`^- \[([^\]]+)\]\(#([a-z0-9-]+)\)$`)
	bodyURLRE  = regexp.MustCompile(`https://[^\s)>\]]+`)
	headingRE  = regexp.MustCompile(`^## (.+)$`)
	slugStrip  = regexp.MustCompile(`[^a-z0-9 -]`)
	roleOfName = func(n string) string { return "belai:" + strings.TrimPrefix(n, "belai-") }
)

func slug(h string) string {
	return strings.ReplaceAll(slugStrip.ReplaceAllString(strings.ToLower(h), ""), " ", "-")
}

func TestBuiltinSkillsAreTheSixWorkers(t *testing.T) {
	var got []string
	for _, e := range Builtin() {
		got = append(got, e.Name)
	}
	if strings.Join(got, ",") != strings.Join(builtinNames, ",") {
		t.Fatalf("builtin skills = %v, want %v", got, builtinNames)
	}
}

// TestBuiltinSkillsFollowTheRules holds every shipped skill to the format:
// specification front matter, Belai's metadata, a contents list first, and no
// more than 100 lines.
func TestBuiltinSkillsFollowTheRules(t *testing.T) {
	for _, e := range Builtin() {
		e := e
		t.Run(e.Name, func(t *testing.T) {
			doc, err := BuiltinDoc(e.Name)
			if err != nil {
				t.Fatal(err)
			}
			m, err := ValidateSkill(doc)
			if err != nil {
				t.Fatal(err)
			}
			if !ValidSpecName(m.Name) || !ReservedName(m.Name) {
				t.Errorf("name %q", m.Name)
			}
			if len(m.Description) < 80 || len(m.Description) > 1024 {
				t.Errorf("description is %d bytes; want 80 to 1024 and say what it does and when to use it", len(m.Description))
			}
			if m.Compatibility == "" || len(m.Compatibility) > 500 {
				t.Errorf("compatibility must name the environment the skill expects (at most 500 bytes)")
			}
			if len(m.AllowedTools) == 0 {
				t.Error("allowed-tools is empty")
			}
			if err := CheckMetadataBounds(m.Metadata); err != nil {
				t.Error(err)
			}
			for _, k := range []string{MetaRole, MetaNiche, MetaContexts, MetaResources, MetaUpdated} {
				if m.Metadata[k] == "" {
					t.Errorf("metadata %s is missing", k)
				}
			}
			if m.Metadata[MetaRole] != roleOfName(m.Name) {
				t.Errorf("belai.role = %q, want %q", m.Metadata[MetaRole], roleOfName(m.Name))
			}
			if n := len(Contexts(m.Metadata[MetaContexts])); n < 15 {
				t.Errorf("belai.contexts names %d environments; a specialist skill names at least 15", n)
			}
			res := Resources(m.Metadata[MetaResources])
			if len(res) < 15 {
				t.Errorf("belai.resources lists %d links; want at least 15", len(res))
			}
			listed := map[string]bool{}
			for _, u := range res {
				listed[u] = true
			}

			_, body, err := Split(doc)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
			if len(lines) > maxBuiltinBodyLines {
				t.Errorf("body is %d lines; the most is %d", len(lines), maxBuiltinBodyLines)
			}
			checkContents(t, lines)

			for _, u := range bodyURLRE.FindAllString(body, -1) {
				u = strings.TrimRight(u, ".,;:")
				if !listed[u] {
					t.Errorf("body links %s, which belai.resources does not list (the link check reads that list)", u)
				}
			}
			lower := strings.ToLower(doc)
			for _, b := range bannedNames {
				if strings.Contains(lower, b) {
					t.Errorf("the skill names %q; a builtin skill names no other agent product", b)
				}
			}
			if strings.ContainsAny(doc, "—–") {
				t.Error("the skill holds an em or en dash")
			}
		})
	}
}

// checkContents requires the body to open with "## Contents" and a list of links
// to every other "## " heading, in order.
func checkContents(t *testing.T, lines []string) {
	t.Helper()
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "## Contents" {
		t.Error("the body must open with a ## Contents heading")
		return
	}
	i++
	var toc []string
	for ; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			if len(toc) == 0 {
				continue
			}
			break
		}
		m := tocLineRE.FindStringSubmatch(l)
		if m == nil {
			t.Errorf("contents line %q is not - [Title](#anchor)", l)
			continue
		}
		if slug(m[1]) != m[2] {
			t.Errorf("contents anchor #%s does not match the title %q", m[2], m[1])
		}
		toc = append(toc, m[2])
	}
	var heads []string
	for _, l := range lines[i:] {
		if m := headingRE.FindStringSubmatch(l); m != nil {
			heads = append(heads, slug(m[1]))
		}
	}
	if len(toc) < 5 {
		t.Errorf("contents lists %d sections; want at least 5", len(toc))
	}
	if strings.Join(toc, ",") != strings.Join(heads, ",") {
		t.Errorf("contents %v does not match the headings %v", toc, heads)
	}
}
