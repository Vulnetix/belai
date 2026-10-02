package libitem

import (
	"strings"
	"testing"
)

func promptDoc(fm, body string) string { return "---\n" + fm + "---\n\n" + body }

func TestValidatePrompt(t *testing.T) {
	long := strings.Repeat("d", MaxDescriptionBytes)
	cases := []struct {
		name string
		doc  string
		want PromptDoc
		err  string
	}{
		{"name and body only", promptDoc("name: deploy\n", "Deploy it."), PromptDoc{Name: "deploy", Enabled: true, Body: "Deploy it.\n"}, ""},
		{"every field", promptDoc("name: deploy\ndescription: Ship it\norder: 40\nenabled: false\n", "Deploy."), PromptDoc{Name: "deploy", Description: "Ship it", Order: 40, Enabled: false, Body: "Deploy.\n"}, ""},
		{"order 0 is allowed", promptDoc("name: a\norder: 0\n", "x"), PromptDoc{Name: "a", Enabled: true, Body: "x\n"}, ""},
		{"order 999 is allowed", promptDoc("name: a\norder: 999\n", "x"), PromptDoc{Name: "a", Order: 999, Enabled: true, Body: "x\n"}, ""},
		{"order 1000 is refused", promptDoc("name: a\norder: 1000\n", "x"), PromptDoc{}, "between 0 and 999"},
		{"order -1 is refused", promptDoc("name: a\norder: -1\n", "x"), PromptDoc{}, "between 0 and 999"},
		{"order that is not a number", promptDoc("name: a\norder: first\n", "x"), PromptDoc{}, "whole number"},
		{"order with a fraction", promptDoc("name: a\norder: 1.5\n", "x"), PromptDoc{}, "whole number"},
		{"enabled that is not a bool", promptDoc("name: a\nenabled: 1\n", "x"), PromptDoc{}, "true or false"},
		{"enabled in capitals", promptDoc("name: a\nenabled: FALSE\n", "x"), PromptDoc{Name: "a", Body: "x\n"}, ""},
		{"description at the limit", promptDoc("name: a\ndescription: "+long+"\n", "x"), PromptDoc{Name: "a", Description: long, Enabled: true, Body: "x\n"}, ""},
		{"description over the limit", promptDoc("name: a\ndescription: "+long+"d\n", "x"), PromptDoc{}, "301 bytes"},
		{"missing name", promptDoc("description: d\n", "x"), PromptDoc{}, `"name" is missing`},
		{"unknown key", promptDoc("name: a\ntags: x\n", "x"), PromptDoc{}, `unknown front-matter field "tags"`},
		{"a skill key is not a prompt key", promptDoc("name: a\nlicense: MIT\n", "x"), PromptDoc{}, `unknown front-matter field "license"`},
		{"duplicate key", promptDoc("name: a\nname: b\n", "x"), PromptDoc{}, "given twice"},
		{"empty body", promptDoc("name: a\n", ""), PromptDoc{}, "body is empty"},
		{"no front matter", "Deploy it.\n", PromptDoc{}, "front-matter"},
		{"name with an uppercase letter", promptDoc("name: Deploy\n", "x"), PromptDoc{}, "lowercase"},
		{"name at the limit", promptDoc("name: "+strings.Repeat("a", 64)+"\n", "x"), PromptDoc{Name: strings.Repeat("a", 64), Enabled: true, Body: "x\n"}, ""},
		{"name over the limit", promptDoc("name: "+strings.Repeat("a", 65)+"\n", "x"), PromptDoc{}, "lowercase"},
		{"a body with its own --- rule", promptDoc("name: a\n", "above\n\n---\n\nbelow"), PromptDoc{Name: "a", Enabled: true, Body: "above\n\n---\n\nbelow\n"}, ""},
		{"a body that starts with dashes", promptDoc("name: a\n", "--- not a delimiter"), PromptDoc{Name: "a", Enabled: true, Body: "--- not a delimiter\n"}, ""},
		{"document at the limit", promptDoc("name: a\n", strings.Repeat("x", 32<<10-len(promptDoc("name: a\n", ""))-1)), PromptDoc{}, ""},
		{"document over the limit", promptDoc("name: a\n", strings.Repeat("x", 32<<10)), PromptDoc{}, "larger than 32768"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Prompt, []byte(c.doc))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want one naming %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if c.want.Name == "" {
				return
			}
			got, err := ParsePrompt(it.Doc)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("parsed %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestComposePromptRoundTrips(t *testing.T) {
	cases := []PromptDoc{
		{Name: "deploy", Enabled: true, Body: "Deploy.\n"},
		{Name: "deploy", Description: "Ship it", Order: 20, Enabled: true, Body: "Deploy.\n\nCarefully.\n"},
		{Name: "off", Order: 990, Enabled: false, Body: "x\n"},
		{Name: "flat", Description: "one\n two\tthree", Enabled: true, Body: "x\n"},
	}
	for _, c := range cases {
		doc, err := ComposePrompt(c)
		if err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		got, err := ParsePrompt(doc)
		if err != nil {
			t.Fatal(err)
		}
		want := c
		want.Description = strings.Join(strings.Fields(c.Description), " ")
		if got != want {
			t.Errorf("round trip: %+v, want %+v", got, want)
		}
		if again, _ := ComposePrompt(got); string(again) != string(doc) {
			t.Errorf("compose is not stable:\n%s\n%s", doc, again)
		}
		if canon, _ := CanonicalMarkdown(doc); string(canon) != string(doc) {
			t.Errorf("composed bytes are not canonical")
		}
	}
	if _, err := ComposePrompt(PromptDoc{Name: "Bad", Body: "x"}); err == nil {
		t.Error("a bad name composed")
	}
	if _, err := ComposePrompt(PromptDoc{Name: "ok", Body: " "}); err == nil {
		t.Error("an empty body composed")
	}
}

// The default form drops what the defaults say, so a prompt nobody customised
// has the shortest document.
func TestComposePromptOmitsDefaults(t *testing.T) {
	doc, err := ComposePrompt(PromptDoc{Name: "a", Enabled: true, Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != "---\nname: a\n---\n\nx\n" {
		t.Errorf("doc = %q", doc)
	}
}
