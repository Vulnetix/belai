package libitem

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

func budgetN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"provider":"p%d","model":"m","scope":"day","tokens":1}`, i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestValidateBudget(t *testing.T) {
	one := func(entry string) string { return `{"name":"team","budgets":[` + entry + `]}` }
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := []struct {
		name string
		doc  string
		err  string
	}{
		{"one budget", one(`{"provider":"anthropic","model":"claude-sonnet-5-5","scope":"day","tokens":1000000}`), ""},
		{"all three scopes", `{"name":"team","budgets":[{"provider":"a","model":"m","scope":"session","tokens":1},{"provider":"a","model":"m","scope":"day","tokens":2},{"provider":"a","model":"m","scope":"month","tokens":3}]}`, ""},
		{"an empty set", `{"name":"team","budgets":[]}`, ""},
		{"with the footer settings", `{"name":"team","budgets":[],"cycle_seconds":10,"warn":true}`, ""},
		{"a name at the limit", `{"name":"` + long(64) + `","budgets":[]}`, ""},

		{"no name", `{"budgets":[]}`, "name is required"},
		{"an uppercase name", `{"name":"Team","budgets":[]}`, "is not valid"},
		{"no budgets key", `{"name":"team"}`, "budget.budgets is required"},
		{"budgets as an object", `{"name":"team","budgets":{}}`, "must be a list"},
		{"budgets as null", `{"name":"team","budgets":null}`, "must be a list"},
		{"100 budgets", `{"name":"team","budgets":` + budgetN(100) + `}`, ""},
		{"101 budgets", `{"name":"team","budgets":` + budgetN(101) + `}`, "has 101 entries"},
		{"an unknown top-level key", `{"name":"team","budgets":[],"currency":"usd"}`, `unknown field "currency"`},
		{"an unknown entry key", one(`{"provider":"a","model":"m","scope":"day","tokens":1,"warn":0.8}`), `unknown field "warn"`},
		{"an entry that is not an object", `{"name":"team","budgets":["x"]}`, "must be an object"},

		{"no provider", one(`{"model":"m","scope":"day","tokens":1}`), "provider is required"},
		{"a blank provider", one(`{"provider":"  ","model":"m","scope":"day","tokens":1}`), "provider is required"},
		{"no model", one(`{"provider":"a","scope":"day","tokens":1}`), "model is required"},
		{"a provider at the limit", one(`{"provider":"` + long(128) + `","model":"m","scope":"day","tokens":1}`), ""},
		{"a provider over the limit", one(`{"provider":"` + long(129) + `","model":"m","scope":"day","tokens":1}`), "provider is 129 bytes"},
		{"a model with a control character", one(`{"provider":"a","model":"m\u0007","scope":"day","tokens":1}`), "holds a control character"},
		{"a provider that is not a string", one(`{"provider":1,"model":"m","scope":"day","tokens":1}`), "must be a string"},

		{"an unknown scope", one(`{"provider":"a","model":"m","scope":"week","tokens":1}`), "must be session, day or month"},
		{"no scope", one(`{"provider":"a","model":"m","tokens":1}`), "must be session, day or month"},
		{"a scope in capitals", one(`{"provider":"a","model":"m","scope":"DAY","tokens":1}`), "must be session, day or month"},

		{"tokens 0", one(`{"provider":"a","model":"m","scope":"day","tokens":0}`), "from 1 to"},
		{"tokens negative", one(`{"provider":"a","model":"m","scope":"day","tokens":-5}`), "from 1 to"},
		{"tokens at the limit", one(`{"provider":"a","model":"m","scope":"day","tokens":9007199254740991}`), ""},
		{"tokens over the limit", one(`{"provider":"a","model":"m","scope":"day","tokens":9007199254740992}`), "from 1 to"},
		{"tokens as an exponent", one(`{"provider":"a","model":"m","scope":"day","tokens":1e6}`), "whole number"},
		{"tokens with a fraction", one(`{"provider":"a","model":"m","scope":"day","tokens":1.5}`), "whole number"},
		{"tokens as a string", one(`{"provider":"a","model":"m","scope":"day","tokens":"5"}`), "whole number"},
		{"no tokens", one(`{"provider":"a","model":"m","scope":"day"}`), "tokens is required"},

		{"the same budget twice", `{"name":"team","budgets":[{"provider":"a","model":"m","scope":"day","tokens":1},{"provider":"a","model":"m","scope":"day","tokens":2}]}`, "is already set"},
		{"the same model in two scopes", `{"name":"team","budgets":[{"provider":"a","model":"m","scope":"day","tokens":1},{"provider":"a","model":"m","scope":"month","tokens":2}]}`, ""},
		{"a/b + c is a/b/c", `{"name":"team","budgets":[{"provider":"a/b","model":"c","scope":"day","tokens":1},{"provider":"a","model":"b/c","scope":"day","tokens":2}]}`, "is already set"},

		{"cycle 0", `{"name":"team","budgets":[],"cycle_seconds":0}`, ""},
		{"cycle 1", `{"name":"team","budgets":[],"cycle_seconds":1}`, ""},
		{"cycle at the limit", `{"name":"team","budgets":[],"cycle_seconds":86400}`, ""},
		{"cycle over the limit", `{"name":"team","budgets":[],"cycle_seconds":86401}`, "from 0 to 86400"},
		{"cycle negative", `{"name":"team","budgets":[],"cycle_seconds":-1}`, "from 0 to 86400"},
		{"cycle as a string", `{"name":"team","budgets":[],"cycle_seconds":"10"}`, "whole number"},
		{"warn as a number", `{"name":"team","budgets":[],"warn":0.8}`, "must be true or false"},
		{"warn as a string", `{"name":"team","budgets":[],"warn":"yes"}`, "must be true or false"},
		{"warn false", `{"name":"team","budgets":[],"warn":false}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Budget, []byte(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Budget || it.Name == "" {
					t.Errorf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
		})
	}
}

func TestParseBudgetAndBudgetDocument(t *testing.T) {
	it, err := Validate(Budget, []byte(`{"name":"team","budgets":[{"provider":"a","model":"m","scope":"day","tokens":500},{"provider":"b","model":"n","scope":"session","tokens":7}],"cycle_seconds":15,"warn":true}`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseBudget(it.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "team" || len(d.Budgets) != 2 || d.Budgets[0] != (config.TokenBudget{Provider: "a", Model: "m", Scope: "day", Tokens: 500}) ||
		d.CycleSeconds == nil || *d.CycleSeconds != 15 || d.Warn == nil || !*d.Warn {
		t.Fatalf("doc = %+v", d)
	}
	// The settings that produced a document export it back byte for byte.
	cycle, warn := 15, true
	s := config.Settings{TokenBudgets: d.Budgets, UI: &config.UISettings{BudgetCycleSeconds: &cycle, BudgetWarn: &warn}}
	b, err := encodeCanonical(BudgetDocument("team", s))
	if err != nil || string(b) != string(it.Doc) {
		t.Fatalf("export = %q (%v), want %q", b, err, it.Doc)
	}
	// Footer settings that are not set are left out.
	bare, _ := encodeCanonical(BudgetDocument("team", config.Settings{TokenBudgets: d.Budgets}))
	if strings.Contains(string(bare), "cycle_seconds") || strings.Contains(string(bare), "warn") {
		t.Errorf("unset footer settings were written: %s", bare)
	}
	// An empty set exports an empty list, not null.
	empty, _ := encodeCanonical(BudgetDocument("x", config.Settings{}))
	if string(empty) != `{"budgets":[],"name":"x"}`+"\n" {
		t.Errorf("empty = %s", empty)
	}
	if _, err := Validate(Budget, empty); err != nil {
		t.Errorf("an empty export does not validate: %v", err)
	}
}

func TestValidateRewrite(t *testing.T) {
	rules := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"match":"tool%d","replace":"other%d"}`, i, i)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	doc := func(rule string) string { return `{"name":"bash_rewrite","rules":[` + rule + `]}` }
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("w ", n)) }
	cases := []struct {
		name string
		doc  string
		err  string
	}{
		{"one rule", doc(`{"match":"npm","replace":"pnpm"}`), ""},
		{"two words each", doc(`{"match":"npm install","replace":"pnpm add"}`), ""},
		{"no rules", `{"name":"bash_rewrite","rules":[]}`, ""},
		{"enabled false", `{"name":"bash_rewrite","enabled":false,"rules":[]}`, ""},
		{"64 rules", `{"name":"bash_rewrite","rules":` + rules(64) + `}`, ""},
		{"65 rules", `{"name":"bash_rewrite","rules":` + rules(65) + `}`, "has 65 entries"},
		{"a path word", doc(`{"match":"python","replace":"/usr/bin/python3"}`), ""},
		{"all the plain characters", doc(`{"match":"a","replace":"b x.y_z-w/v@u+t:s=r,q"}`), ""},

		{"another name", `{"name":"rewrite","rules":[]}`, "it must be bash_rewrite"},
		{"no name", `{"rules":[]}`, "name is required"},
		{"no rules key", `{"name":"bash_rewrite"}`, "rewrite.rules is required"},
		{"an unknown key", `{"name":"bash_rewrite","rules":[],"mode":"x"}`, `unknown field "mode"`},
		{"an unknown rule key", doc(`{"match":"a","replace":"b","regex":true}`), `unknown field "regex"`},
		{"enabled not a bool", `{"name":"bash_rewrite","enabled":"no","rules":[]}`, "must be true or false"},
		{"a rule that is not an object", `{"name":"bash_rewrite","rules":["npm"]}`, "must be an object"},
		{"no match", doc(`{"replace":"b"}`), "match is required"},
		{"no replace", doc(`{"match":"a"}`), "replace is required"},
		{"an empty side", doc(`{"match":"  ","replace":"b"}`), "is empty"},
		{"8 words", doc(`{"match":"a","replace":"` + words(8) + `"}`), ""},
		{"9 words", doc(`{"match":"a","replace":"` + words(9) + `"}`), "more than 8 words"},
		{"a word at the limit", doc(`{"match":"a","replace":"` + strings.Repeat("w", 128) + `"}`), ""},
		{"a word over the limit", doc(`{"match":"a","replace":"` + strings.Repeat("w", 129) + `"}`), "must be plain"},
		{"a pipe", doc(`{"match":"a","replace":"b | c"}`), "must be plain"},
		{"a semicolon", doc(`{"match":"a","replace":"b;c"}`), "must be plain"},
		{"a dollar", doc(`{"match":"a","replace":"$HOME"}`), "must be plain"},
		{"a quote", doc(`{"match":"a","replace":"'b'"}`), "must be plain"},
		{"a backtick", doc(`{"match":"a","replace":"` + "`b`" + `"}`), "must be plain"},
		{"a glob", doc(`{"match":"a","replace":"b*"}`), "must be plain"},
		{"a tab is a control character", doc(`{"match":"a\tb","replace":"c"}`), "separate words with spaces"},
		{"a newline is a control character", doc(`{"match":"a","replace":"b\nc"}`), "separate words with spaces"},
		{"a first word that is an option", doc(`{"match":"-x","replace":"y"}`), "must start with a command name"},
		{"a first word that is an assignment", doc(`{"match":"a","replace":"FOO=bar baz"}`), "must start with a command name"},
		{"the same on both sides", doc(`{"match":"npm i","replace":"npm  i"}`), "match and replace are the same"},
		{"a non-string match", doc(`{"match":1,"replace":"b"}`), "must be a string"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Rewrite, []byte(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Name != "bash_rewrite" {
					t.Errorf("name = %q", it.Name)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
		})
	}
}

func TestParseRewriteAndRewriteDocument(t *testing.T) {
	it, err := Validate(Rewrite, []byte(`{"name":"bash_rewrite","enabled":false,"rules":[{"match":"npm install","replace":"pnpm add"},{"match":"npm","replace":"pnpm"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseRewrite(it.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Enabled == nil || *d.Enabled || len(d.Rules) != 2 || d.Rules[0] != (config.BashRewriteRule{Match: "npm install", Replace: "pnpm add"}) {
		t.Fatalf("doc = %+v", d)
	}
	off := false
	s := config.Settings{BashRewrite: &config.BashRewriteSettings{Enabled: &off, Rules: d.Rules}}
	b, _ := encodeCanonical(RewriteDocument(s))
	if string(b) != string(it.Doc) {
		t.Fatalf("export = %q, want %q", b, it.Doc)
	}
	// With no setting, the export is an empty table that validates.
	empty, _ := encodeCanonical(RewriteDocument(config.Settings{}))
	if string(empty) != `{"name":"bash_rewrite","rules":[]}`+"\n" {
		t.Errorf("empty = %s", empty)
	}
	if _, err := Validate(Rewrite, empty); err != nil {
		t.Errorf("an empty export does not validate: %v", err)
	}
}

func TestLibraryItemsPageStatesTheBudgetAndRewriteRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/library-items.md")), " ")
	for _, want := range []string{
		"`budgets` | required, 0 to 100 entries",
		"whole number 1 to 2^53 - 1",
		"optional whole number 0 to 86400",
		"`warn` | optional **boolean**",
		"`rules` | required, ordered, 0 to 64 entries",
		"each 1 to 8 words",
		"1 to 128 bytes of ASCII letters, digits and `. _ - / @ + : = ,`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/library-items.md does not say %q", want)
		}
	}
	if MaxBudgets != 100 || MaxBudgetTokens != 1<<53-1 || MaxBudgetName != 128 || MaxBudgetCycle != 86400 ||
		MaxRewriteRules != 64 || MaxRewriteWords != 8 || MaxRewriteWord != 128 {
		t.Error("a budget or rewrite limit changed; update docs/library-items.md and this test")
	}
	// The host's own limits are the ones the page states.
	if MaxRewriteRules != config.MaxBashRewriteRules {
		t.Errorf("the library takes %d rules and the host %d", MaxRewriteRules, config.MaxBashRewriteRules)
	}
}
