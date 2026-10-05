package libitem

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/skills"
)

func skillDoc(fm, body string) string { return "---\n" + fm + "---\n\n" + body }

const okSkillFM = "name: release\ndescription: Cut a release\n"

func TestValidateSkill(t *testing.T) {
	long := strings.Repeat("d", MaxSkillDescriptionBytes)
	tools := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("T%d", i)
		}
		return "allowed-tools: [" + strings.Join(parts, ", ") + "]\n"
	}
	cases := []struct {
		name string
		doc  string
		err  string // substring of the refusal; "" means accepted
	}{
		{"minimal", skillDoc(okSkillFM, "1. run just check"), ""},
		{"every field", skillDoc(okSkillFM+"license: MIT\ncompatibility: belai\nmetadata: team=platform tier=2\nallowed-tools: [Bash, \"Read\"]\ndisable-model-invocation: true\n", "x"), ""},
		{"quoted values", skillDoc("name: \"release\"\ndescription: 'Cut a release'\n", "x"), ""},
		{"comments and blank lines in the front matter", skillDoc("# why\n\n"+okSkillFM, "x"), ""},
		{"a colon in the description", skillDoc("name: release\ndescription: Cut a release: tag it\n", "x"), ""},
		{"an indented line is trimmed like the loader trims it", skillDoc("name: release\n  description: d\n", "x"), ""},
		{"a body that starts with a list marker", skillDoc(okSkillFM, "- one\n- two"), ""},
		{"CRLF line endings", strings.ReplaceAll(skillDoc(okSkillFM, "x"), "\n", "\r\n"), ""},
		{"a closing line with trailing blanks", "---\n" + okSkillFM + "---  \t\n\nbody\n", ""},
		{"a boolean in any case", skillDoc(okSkillFM+"disable-model-invocation: TRUE\n", "x"), ""},
		{"description at the limit", skillDoc("name: release\ndescription: "+long+"\n", "x"), ""},
		{"description over the limit", skillDoc("name: release\ndescription: "+long+"d\n", "x"), "description is 1025 bytes"},
		{"a bidi override in the description is the gate's business, not the validator's", skillDoc("name: release\ndescription: a‮b\n", "x"), ""},
		{"license at the limit", skillDoc(okSkillFM+"license: "+strings.Repeat("l", MaxSkillLicense)+"\n", "x"), ""},
		{"license over the limit", skillDoc(okSkillFM+"license: "+strings.Repeat("l", MaxSkillLicense+1)+"\n", "x"), "license is over 128 bytes"},
		{"compatibility at the limit", skillDoc(okSkillFM+"compatibility: "+strings.Repeat("c", MaxSkillCompatibility)+"\n", "x"), ""},
		{"compatibility over the limit", skillDoc(okSkillFM+"compatibility: "+strings.Repeat("c", MaxSkillCompatibility+1)+"\n", "x"), "compatibility is over 500 bytes"},
		{"a scalar metadata value at the limit", skillDoc(okSkillFM+"metadata: "+strings.Repeat("m", skills.MaxMetadataValue)+"\n", "x"), ""},
		{"a scalar metadata value over the limit", skillDoc(okSkillFM+"metadata: "+strings.Repeat("m", skills.MaxMetadataValue+1)+"\n", "x"), "is over 1024 bytes"},
		{"metadata as a map", skillDoc(okSkillFM+"metadata:\n  author: example-org\n  version: \"1.0\"\n", "x"), ""},
		{"metadata as a flow map", skillDoc(okSkillFM+"metadata: {a: 1, b: two}\n", "x"), ""},
		{"metadata with a nested value", skillDoc(okSkillFM+"metadata: {a: 1, b: [2]}\n", "x"), "string keys to string values"},
		{"metadata with too many entries", skillDoc(okSkillFM+"metadata:\n"+manyMeta(skills.MaxMetadataEntries+1), "x"), "the most is 32"},
		{"metadata over the total", skillDoc(okSkillFM+"metadata:\n"+bigMeta(), "x"), "the most is 8192"},
		{"an unknown belai key", skillDoc(okSkillFM+"metadata:\n  belai.typo: x\n", "x"), "unknown reserved key"},
		{"belai keys that are valid", skillDoc(okSkillFM+"metadata:\n  belai.role: belai:scout\n  belai.niche: test gaps\n  belai.contexts: Go, Python\n  belai.resources: https://go.dev/doc/\n  belai.updated: 2026-01-31\n", "x"), ""},
		{"belai resources that are not https", skillDoc(okSkillFM+"metadata:\n  belai.resources: http://example.com/\n", "x"), "must be an https URL"},
		{"belai resources with a fragment", skillDoc(okSkillFM+"metadata:\n  belai.resources: https://example.com/a#b\n", "x"), "must not carry a fragment"},
		{"a belai date that is not a date", skillDoc(okSkillFM+"metadata:\n  belai.updated: soon\n", "x"), "must be a date"},
		{"allowed-tools at the limit", skillDoc(okSkillFM+tools(MaxSkillAllowedTools), "x"), ""},
		{"allowed-tools over the limit", skillDoc(okSkillFM+tools(MaxSkillAllowedTools+1), "x"), "allowed-tools has 65 entries"},
		{"an allowed-tools entry at the limit", skillDoc(okSkillFM+"allowed-tools: ["+strings.Repeat("t", MaxSkillTool)+"]\n", "x"), ""},
		{"an allowed-tools entry over the limit", skillDoc(okSkillFM+"allowed-tools: ["+strings.Repeat("t", MaxSkillTool+1)+"]\n", "x"), "an allowed-tools entry is over 128 bytes"},
		{"allowed-tools as the specification's string", skillDoc(okSkillFM+"allowed-tools: Bash(git:*) Read\n", "x"), ""},
		{"allowed-tools as a block list", skillDoc(okSkillFM+"allowed-tools:\n  - Bash\n  - Read\n", "x"), ""},
		{"an empty allowed-tools entry", skillDoc(okSkillFM+"allowed-tools: [Bash, , Read]\n", "x"), "empty list item"},
		{"an empty allowed-tools list", skillDoc(okSkillFM+"allowed-tools: []\n", "x"), ""},
		{"missing name", skillDoc("description: d\n", "x"), "required front-matter field \"name\""},
		{"missing description", skillDoc("name: release\n", "x"), "required front-matter field \"description\""},
		{"empty description", skillDoc("name: release\ndescription:\n", "x"), "required front-matter field \"description\""},
		{"empty name", skillDoc("name:\ndescription: d\n", "x"), "required front-matter field \"name\""},
		{"unknown key", skillDoc(okSkillFM+"version: 2\n", "x"), `unknown front-matter field "version"`},
		{"key in the wrong case", skillDoc("Name: release\ndescription: d\n", "x"), `unknown front-matter field "Name"`},
		{"duplicate key", skillDoc(okSkillFM+"name: other\n", "x"), "appears twice"},
		{"a line that is not key: value", skillDoc(okSkillFM+"just words\n", "x"), "malformed front-matter line"},
		{"a longer line starting with --- is not a closing line", skillDoc(okSkillFM+"----\n", "x"), "malformed front matter line"},
		{"disable-model-invocation that is not a bool", skillDoc(okSkillFM+"disable-model-invocation: maybe\n", "x"), "expected true/false"},
		{"empty body", skillDoc(okSkillFM, ""), "no instructions"},
		{"whitespace body", skillDoc(okSkillFM, "  \n\t\n"), "no instructions"},
		{"no front matter", "just a body\n", "must open with a --- front matter line"},
		{"unterminated front matter", "---\nname: release\ndescription: d\n", "not closed"},
		{"name with an uppercase letter", skillDoc("name: Release\ndescription: d\n", "x"), "is not valid"},
		{"name that starts with a hyphen", skillDoc("name: -release\ndescription: d\n", "x"), "is not valid"},
		{"name that starts with a dot", skillDoc("name: .release\ndescription: d\n", "x"), "is not valid"},
		{"name with a slash", skillDoc("name: a/b\ndescription: d\n", "x"), "is not valid"},
		{"name with a space", skillDoc("name: a b\ndescription: d\n", "x"), "is not valid"},
		{"name with dots and underscores", skillDoc("name: my_skill.v2\ndescription: d\n", "x"), ""},
		{"name at the limit", skillDoc("name: "+strings.Repeat("a", 64)+"\ndescription: d\n", "x"), ""},
		{"name over the limit", skillDoc("name: "+strings.Repeat("a", 65)+"\ndescription: d\n", "x"), "is not valid"},
		{"document at the limit", skillDoc(okSkillFM, strings.Repeat("x", 32<<10-len(skillDoc(okSkillFM, ""))-1)), ""},
		{"document over the limit", skillDoc(okSkillFM, strings.Repeat("x", 32<<10)), "the most is 32768"},
		{"a NUL byte", skillDoc(okSkillFM, "a\x00b"), "NUL"},
		{"a lone carriage return", skillDoc(okSkillFM, "a\rb"), "control character"},
		{"a terminal escape", skillDoc(okSkillFM, "a\x1b[31mb"), "control character"},
		{"a tab is fine", skillDoc(okSkillFM, "a\tb"), ""},
		{"empty document", "", "the document is empty"},
		{"invalid UTF-8", skillDoc(okSkillFM, "a\xffb"), "not valid UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Skill, []byte(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Skill || it.Name == "" || len(it.SHA256) != 64 || it.Doc[len(it.Doc)-1] != '\n' {
					t.Errorf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
			if !IsRefusal(err) {
				t.Errorf("%v is not a refusal", err)
			}
		})
	}
}

// Whatever the library accepts, the loader accepts: the host's own validator
// runs after the library's rules.
func TestAcceptedSkillsPassTheLoader(t *testing.T) {
	doc := skillDoc(okSkillFM+"allowed-tools: [Bash]\nmetadata:\n  a: b\n", "body")
	it, err := Validate(Skill, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	m, err := skills.ValidateSkill(string(it.Doc))
	if err != nil || m.Name != "release" {
		t.Fatalf("loader: %v %+v", err, m)
	}
}

// A document and its CRLF twin are one skill: same bytes, same hash, same name.
func TestSkillSpellingsShareOneHash(t *testing.T) {
	a, err := Validate(Skill, []byte(skillDoc(okSkillFM, "step one\nstep two")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Validate(Skill, []byte(strings.ReplaceAll(skillDoc(okSkillFM, "step one\nstep two\n\n"), "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || a.Name != "release" || b.Name != "release" {
		t.Fatalf("%+v vs %+v", a, b)
	}
}

func TestParseSkillBody(t *testing.T) {
	it, err := Validate(Skill, []byte(skillDoc(okSkillFM, "\n\n- one\n- two\n")))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseSkill(it.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Body != "- one\n- two\n" || d.Description != "Cut a release" || d.Name != "release" {
		t.Errorf("doc = %+v", d)
	}
}

func manyMeta(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  k%d: v\n", i)
	}
	return b.String()
}

// bigMeta is under the entry and value limits and over the total.
func bigMeta() string {
	var b strings.Builder
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&b, "  k%d: %s\n", i, strings.Repeat("v", 1000))
	}
	return b.String()
}
