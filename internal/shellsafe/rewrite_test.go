package shellsafe

import "testing"

func rule(match, replace string) RewriteRule {
	return RewriteRule{Match: splitWords(match), Replace: splitWords(replace)}
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestRewriteCommandPosition(t *testing.T) {
	npm := []RewriteRule{rule("npm", "pnpm")}
	cases := []struct{ in, want string }{
		{"npm install", "pnpm install"},
		{"  npm   install  left", "pnpm   install  left"},
		{`"npm" install`, "pnpm install"},
		{"/usr/bin/npm install", "pnpm install"},
		{"FOO=1 npm test", "FOO=1 pnpm test"},
		{"npm i && npm test", "pnpm i && pnpm test"},
		{"cd x; npm i | tee out", "cd x; pnpm i | tee out"},
		{"(npm i)", "(pnpm i)"},
		{"echo $(npm bin)", "echo $(pnpm bin)"},
		{"npm run build > out.txt", "pnpm run build > out.txt"},
		// Not in command position, or not a fixed word: untouched.
		{"echo npm", "echo npm"},
		{"grep npm file", "grep npm file"},
		{"npmx install", "npmx install"},
		{"$CMD install", "$CMD install"},
		{"n*m install", "n*m install"},
		{"env npm install", "env npm install"},
		{"sudo npm install", "sudo npm install"},
		{"sh -c 'npm install'", "sh -c 'npm install'"},
		{"xargs npm", "xargs npm"},
		{"cat <<EOF\nnpm install\nEOF", "cat <<EOF\nnpm install\nEOF"},
		{"echo 'npm install'", "echo 'npm install'"},
	}
	for _, c := range cases {
		got, _, why := Rewrite(c.in, npm)
		if got != c.want || why != "" {
			t.Errorf("Rewrite(%q) = %q (%s), want %q", c.in, got, why, c.want)
		}
	}
}

func TestRewriteMultiWordMatchAndFirstRuleWins(t *testing.T) {
	rules := []RewriteRule{rule("npm install", "pnpm add"), rule("npm", "pnpm")}
	if got, _, _ := Rewrite("npm install left-pad", rules); got != "pnpm add left-pad" {
		t.Fatalf("got %q", got)
	}
	if got, _, _ := Rewrite("npm test", rules); got != "pnpm test" {
		t.Fatalf("got %q", got)
	}
	// A rewrite is one pass: the output is not fed back through the table.
	chain := []RewriteRule{rule("a", "b"), rule("b", "c")}
	if got, _, _ := Rewrite("a", chain); got != "b" {
		t.Fatalf("got %q", got)
	}
}

func TestRewriteUnparseableAndEmpty(t *testing.T) {
	npm := []RewriteRule{rule("npm", "pnpm")}
	for _, in := range []string{"npm 'unterminated", "npm install &&", "", "npm \x00 x"} {
		if got, applied, _ := Rewrite(in, npm); got != in || len(applied) != 0 {
			t.Errorf("Rewrite(%q) = %q, %v", in, got, applied)
		}
	}
	if got, _, _ := Rewrite("npm i", nil); got != "npm i" {
		t.Fatalf("no rules must change nothing, got %q", got)
	}
}

func TestRewriteResultKeepsTheShapeOfTheLine(t *testing.T) {
	// The result is analysed again and has the same flags and command count.
	in := "FOO=1 npm i && (npm test | tee x) > out 2>&1 &"
	got, applied, why := Rewrite(in, []RewriteRule{rule("npm", "pnpm")})
	if why != "" || len(applied) != 2 {
		t.Fatalf("got %q applied %v why %q", got, applied, why)
	}
	a, b := Analyze(in), Analyze(got)
	if a.Flags != b.Flags || a.Written != b.Written {
		t.Fatalf("shape changed: %v/%d -> %v/%d", a.Flags, a.Written, b.Flags, b.Written)
	}
}

func TestValidRewriteRule(t *testing.T) {
	good := []RewriteRule{rule("npm", "pnpm"), rule("npm install", "pnpm add"), rule("pip", "uv pip"), rule("node", "/usr/bin/node")}
	for _, r := range good {
		if err := ValidRewriteRule(r); err != nil {
			t.Errorf("%v: %v", r, err)
		}
	}
	bad := []RewriteRule{
		{},
		{Match: []string{"npm"}},
		{Replace: []string{"pnpm"}},
		rule("npm", "npm"),
		{Match: []string{"npm"}, Replace: []string{"pnpm;", "rm"}},
		{Match: []string{"npm"}, Replace: []string{"$(x)"}},
		{Match: []string{"npm"}, Replace: []string{"a|b"}},
		{Match: []string{"npm"}, Replace: []string{"a b"}},
		{Match: []string{"npm"}, Replace: []string{"'x'"}},
		{Match: []string{"npm"}, Replace: []string{"x>y"}},
		{Match: []string{"npm"}, Replace: []string{"~x"}},
		{Match: []string{"npm"}, Replace: []string{"*"}},
		{Match: []string{"npm"}, Replace: []string{"-rf"}},
		{Match: []string{"npm"}, Replace: []string{"A=1", "pnpm"}},
		{Match: []string{"npm"}, Replace: []string{"pnpm\n"}},
		{Match: make([]string, MaxRewriteWords+1), Replace: []string{"x"}},
	}
	for _, r := range bad {
		if ValidRewriteRule(r) == nil {
			t.Errorf("%q was accepted", r)
		}
	}
}
