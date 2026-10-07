package libitem

import (
	"strings"
	"testing"
)

const okCommandFM = "name: triage\ndescription: Triage a finding\n"

func TestValidateCommand(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		err  string // substring of the refusal; "" means accepted
	}{
		{"minimal", skillDoc(okCommandFM, "Triage $ARGUMENTS."), ""},
		{"argument hint and tools", skillDoc(okCommandFM+"argument-hint: <finding id>\nallowed-tools: Read Grep\n", "x"), ""},
		{"every skill field", skillDoc(okCommandFM+"license: MIT\ncompatibility: belai\nmetadata:\n  a: b\nallowed-tools: [Bash]\ndisable-model-invocation: true\nargument-hint: x\n", "x"), ""},
		{"a hint at the limit", skillDoc(okCommandFM+"argument-hint: "+strings.Repeat("h", MaxCommandArgumentHint)+"\n", "x"), ""},
		{"a hint over the limit", skillDoc(okCommandFM+"argument-hint: "+strings.Repeat("h", MaxCommandArgumentHint+1)+"\n", "x"), "argument-hint is 257 bytes"},
		{"a belai- name is not reserved", skillDoc("name: belai-triage\ndescription: d\n", "x"), ""},
		{"belai metadata keys carry no rules", skillDoc(okCommandFM+"metadata:\n  belai.typo: x\n", "x"), ""},
		{"an unknown key", skillDoc(okCommandFM+"model: fast\n", "x"), `unknown front-matter field "model"`},
		{"a duplicate key", skillDoc(okCommandFM+"argument-hint: a\nargument-hint: b\n", "x"), "appears twice"},
		{"missing description", skillDoc("name: triage\n", "x"), `required front-matter field "description"`},
		{"missing name", skillDoc("description: d\n", "x"), `required front-matter field "name"`},
		{"a bad name", skillDoc("name: Triage\ndescription: d\n", "x"), "is not valid"},
		{"a name with a slash", skillDoc("name: a/b\ndescription: d\n", "x"), "is not valid"},
		{"a bidi override in the hint", skillDoc(okCommandFM+"argument-hint: a‮b\n", "x"), "bidirectional"},
		{"empty body", skillDoc(okCommandFM, ""), "no instructions"},
		{"no front matter", "just a body\n", "must open with a --- front matter line"},
		{"allowed-tools over the limit", skillDoc(okCommandFM+"allowed-tools: ["+strings.Repeat("t, ", MaxSkillAllowedTools)+"t]\n", "x"), "allowed-tools has 65 entries"},
		{"document over the limit", skillDoc(okCommandFM, strings.Repeat("x", 32<<10)), "the most is 32768"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Command, []byte(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Command || it.Name == "" || len(it.SHA256) != 64 {
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

// A skill does not take argument-hint, and a command is not a skill: the field
// sets differ in exactly that key.
func TestSkillRefusesArgumentHint(t *testing.T) {
	if _, err := Validate(Skill, []byte(skillDoc(okSkillFM+"argument-hint: x\n", "x"))); err == nil || !strings.Contains(err.Error(), "argument-hint") {
		t.Errorf("a skill took argument-hint: %v", err)
	}
}

func TestParseAndComposeCommand(t *testing.T) {
	it, err := Validate(Command, []byte(skillDoc(okCommandFM+"argument-hint: <id>\nallowed-tools: Read Grep\n", "\n\nLook at $1.\n")))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseCommand(it.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "triage" || d.Description != "Triage a finding" || d.ArgumentHint != "<id>" || d.Body != "Look at $1.\n" || len(d.AllowedTools) != 2 {
		t.Fatalf("doc = %+v", d)
	}
	out, err := ComposeCommand(d)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseCommand(out)
	if err != nil || again.Name != d.Name || again.Body != d.Body || again.ArgumentHint != d.ArgumentHint {
		t.Fatalf("round trip: %v %+v", err, again)
	}
	if _, err := ComposeCommand(CommandDoc{Name: "Bad Name", Description: "d", Body: "x"}); err == nil {
		t.Error("composed a command with an invalid name")
	}
}

// A command and its CRLF twin are one command: same bytes, same hash.
func TestCommandSpellingsShareOneHash(t *testing.T) {
	a, err := Validate(Command, []byte(skillDoc(okCommandFM, "step one\nstep two")))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Validate(Command, []byte(strings.ReplaceAll(skillDoc(okCommandFM, "step one\nstep two\n\n"), "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 {
		t.Fatalf("%+v vs %+v", a, b)
	}
}
