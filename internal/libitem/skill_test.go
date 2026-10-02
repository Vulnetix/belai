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
	long := strings.Repeat("d", MaxDescriptionBytes)
	tools64 := make([]string, MaxSkillAllowedTools)
	for i := range tools64 {
		tools64[i] = fmt.Sprintf("T%d", i)
	}
	tools65 := append(append([]string{}, tools64...), "extra")
	meta := func(n int) string {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf("k%d: v", i))
		}
		return "metadata: {" + strings.Join(parts, ", ") + "}\n"
	}
	cases := []struct {
		name string
		doc  string
		err  string // substring of the refusal; "" means accepted
	}{
		{"minimal", skillDoc(okSkillFM, "1. run just check"), ""},
		{"every field", skillDoc(okSkillFM+"license: MIT\ncompatibility: belai\nmetadata: {team: platform, tier: 2}\nallowed-tools: [Bash, Read]\ndisable-model-invocation: true\n", "x"), ""},
		{"quoted values", skillDoc("name: \"release\"\ndescription: 'Cut a release'\n", "x"), ""},
		{"comments and blank lines in the front matter", skillDoc("# why\n\n"+okSkillFM, "x"), ""},
		{"a colon in the description", skillDoc("name: release\ndescription: Cut a release: tag it\n", "x"), ""},
		{"a body that starts with a list marker", skillDoc(okSkillFM, "- one\n- two"), ""},
		{"CRLF line endings", strings.ReplaceAll(skillDoc(okSkillFM, "x"), "\n", "\r\n"), ""},
		{"description at the limit", skillDoc("name: release\ndescription: "+long+"\n", "x"), ""},
		{"description over the limit", skillDoc("name: release\ndescription: "+long+"d\n", "x"), "description is 301 bytes"},
		{"allowed-tools at the limit", skillDoc(okSkillFM+"allowed-tools: ["+strings.Join(tools64, ", ")+"]\n", "x"), ""},
		{"allowed-tools over the limit", skillDoc(okSkillFM+"allowed-tools: ["+strings.Join(tools65, ", ")+"]\n", "x"), "at most 64"},
		{"metadata at the limit", skillDoc(okSkillFM+meta(MaxSkillMetadataKeys), "x"), ""},
		{"metadata over the limit", skillDoc(okSkillFM+meta(MaxSkillMetadataKeys+1), "x"), "at most 32"},
		{"metadata that is not a map", skillDoc(okSkillFM+"metadata: platform\n", "x"), "one-line {key: value} map"},
		{"metadata entry without a value separator", skillDoc(okSkillFM+"metadata: {team}\n", "x"), "not key: value"},
		{"metadata with a repeated key", skillDoc(okSkillFM+"metadata: {a: 1, a: 2}\n", "x"), "given twice"},
		{"empty metadata map", skillDoc(okSkillFM+"metadata: {}\n", "x"), ""},
		{"missing name", skillDoc("description: d\n", "x"), `"name" missing`},
		{"missing description", skillDoc("name: release\n", "x"), `"description" missing`},
		{"empty description", skillDoc("name: release\ndescription:\n", "x"), `"description" missing`},
		{"unknown key", skillDoc(okSkillFM+"version: 2\n", "x"), `unknown front-matter field "version"`},
		{"key in the wrong case", skillDoc("Name: release\ndescription: d\n", "x"), `unknown front-matter field "Name"`},
		{"duplicate key", skillDoc(okSkillFM+"name: other\n", "x"), "given twice"},
		{"indented key", skillDoc("name: release\n  description: d\n", "x"), "indented"},
		{"a line that is not key: value", skillDoc(okSkillFM+"just words\n", "x"), "malformed"},
		{"a stray --- line inside the front matter", skillDoc(okSkillFM+"----\n", "x"), "stray ---"},
		{"allowed-tools that is not a list", skillDoc(okSkillFM+"allowed-tools: Bash\n", "x"), "expected list"},
		{"an empty allowed-tools entry", skillDoc(okSkillFM+"allowed-tools: [Bash, , Read]\n", "x"), "empty list item"},
		{"disable-model-invocation that is not a bool", skillDoc(okSkillFM+"disable-model-invocation: maybe\n", "x"), "true/false"},
		{"empty body", skillDoc(okSkillFM, ""), "body is empty"},
		{"whitespace body", skillDoc(okSkillFM, "  \n\t\n"), "body is empty"},
		{"no front matter", "just a body\n", "front-matter"},
		{"unterminated front matter", "---\nname: release\ndescription: d\n", "not closed"},
		{"name with an uppercase letter", skillDoc("name: Release\ndescription: d\n", "x"), "lowercase"},
		{"name that starts with a hyphen", skillDoc("name: -release\ndescription: d\n", "x"), "lowercase"},
		{"name that starts with a dot", skillDoc("name: .release\ndescription: d\n", "x"), "lowercase"},
		{"name with a slash", skillDoc("name: a/b\ndescription: d\n", "x"), "lowercase"},
		{"name with a space", skillDoc("name: a b\ndescription: d\n", "x"), "lowercase"},
		{"name with dots and underscores", skillDoc("name: my_skill.v2\ndescription: d\n", "x"), ""},
		{"name at the limit", skillDoc("name: "+strings.Repeat("a", 64)+"\ndescription: d\n", "x"), ""},
		{"name over the limit", skillDoc("name: "+strings.Repeat("a", 65)+"\ndescription: d\n", "x"), "lowercase"},
		{"description with a bidi override", skillDoc("name: release\ndescription: a‮b\n", "x"), "invisible"},
		{"document at the limit", skillDoc(okSkillFM, strings.Repeat("x", 32<<10-len(skillDoc(okSkillFM, ""))-1)), ""},
		{"document over the limit", skillDoc(okSkillFM, strings.Repeat("x", 32<<10)), "larger than 32768"},
		{"NUL byte", skillDoc(okSkillFM, "a\x00b"), "NUL"},
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

// Whatever the library accepts, the loader accepts: the host's own validator is
// the source of truth and Validate runs it.
func TestAcceptedSkillsPassTheLoader(t *testing.T) {
	doc := skillDoc(okSkillFM+"allowed-tools: [Bash]\nmetadata: {a: b}\n", "body")
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
	if d.Body != "- one\n- two\n" || d.Description != "Cut a release" {
		t.Errorf("doc = %+v", d)
	}
}
