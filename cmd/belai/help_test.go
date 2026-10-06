package main

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// mainFlagNames returns the flags main defines on the global flag set (calls on
// the package `flag`, not on a subcommand's own set), by walking main.go.
func mainFlagNames(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := map[string]bool{"String": true, "Bool": true, "Int": true, "Int64": true, "Uint": true, "Float64": true, "Duration": true}
	second := map[string]bool{"Var": true, "StringVar": true, "BoolVar": true, "IntVar": true, "Int64Var": true, "UintVar": true, "Float64Var": true, "DurationVar": true}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "flag" {
			return true
		}
		idx := -1
		switch {
		case first[sel.Sel.Name]:
			idx = 0
		case second[sel.Sel.Name]:
			idx = 1
		}
		if idx < 0 || len(call.Args) <= idx {
			return true
		}
		if lit, ok := call.Args[idx].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				seen[s] = true
			}
		}
		return true
	})
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestEveryTopLevelFlagIsGrouped keeps `belai -help` complete: a flag added to
// main without a place in flagGroups (or an alias entry) fails here, and a group
// may not name a flag that no longer exists.
func TestEveryTopLevelFlagIsGrouped(t *testing.T) {
	defined := mainFlagNames(t)
	if len(defined) < 40 {
		t.Fatalf("only %d flags found in main.go: %v", len(defined), defined)
	}
	shown := map[string]string{}
	for _, g := range flagGroups {
		for _, n := range g.names {
			if prev, dup := shown[n]; dup {
				t.Errorf("-%s is in both %q and %q", n, prev, g.title)
			}
			shown[n] = g.title
		}
	}
	for _, long := range sortedKeys(flagAliases) {
		for _, a := range flagAliases[long] {
			shown[a] = "alias of " + long
		}
	}
	isDefined := map[string]bool{}
	for _, n := range defined {
		isDefined[n] = true
		if shown[n] == "" {
			t.Errorf("-%s is defined in main.go but not shown by `belai -help`: add it to flagGroups", n)
		}
	}
	for n := range shown {
		if !isDefined[n] {
			t.Errorf("flagGroups shows -%s, which main.go does not define", n)
		}
	}
}

func sortedKeys(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryTopLevelCommandIsListed holds the command table to the commands main
// dispatches.
func TestEveryTopLevelCommandIsListed(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range topCommands {
		listed[c.name] = true
		if c.summary == "" {
			t.Errorf("command %s has no summary", c.name)
		}
	}
	hidden := map[string]bool{}
	for _, h := range hiddenCommands {
		hidden[h] = true
	}
	dispatched := map[string]bool{}
	for _, c := range topLevelCommands(t) {
		dispatched[c] = true
		if !listed[c] && !hidden[c] {
			t.Errorf("main dispatches `belai %s`, but `belai -help` neither lists nor hides it", c)
		}
	}
	for c := range listed {
		if !dispatched[c] {
			t.Errorf("`belai -help` lists %s, which main does not dispatch", c)
		}
	}
}

// TestHelpExamplesUseRealFlags checks each example's flags against main's.
func TestHelpExamplesUseRealFlags(t *testing.T) {
	real := map[string]bool{}
	for _, n := range mainFlagNames(t) {
		real[n] = true
	}
	for _, long := range sortedKeys(flagAliases) {
		for _, a := range flagAliases[long] {
			real[a] = true
		}
	}
	for _, e := range helpExamples {
		fields := strings.Fields(e.cmd)
		if len(fields) < 2 || fields[0] != "belai" {
			continue
		}
		sub := false
		for _, c := range topCommands {
			sub = sub || c.name == fields[1]
		}
		if sub {
			continue
		}
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") && !real[strings.TrimLeft(f, "-")] {
				t.Errorf("example %q uses %s, which is not a flag", e.cmd, f)
			}
		}
	}
}

func testFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("belai", flag.ContinueOnError)
	for _, g := range flagGroups {
		for _, n := range g.names {
			fs.String(n, "", "description of "+n+" that is long enough to wrap onto a second line of the help")
		}
	}
	fs.Bool("resume-ish", true, "unused")
	return fs
}

func TestPrintHelpShape(t *testing.T) {
	var b bytes.Buffer
	printHelp(&b, testFlagSet())
	out := b.String()
	for _, want := range []string{"usage: belai", "Commands:", "Examples:", "Session flags:", "-resume, -r string", "-continue, -c string", "-help, -h"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	for _, c := range topCommands {
		if !strings.Contains(out, "  "+c.name+" ") {
			t.Errorf("help does not list command %s", c.name)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if len(line) > helpWidth+2 {
			t.Errorf("line %d is %d columns: %q", i+1, len(line), line)
		}
	}
}

func TestWrap(t *testing.T) {
	got := wrap("one two three four", 9)
	want := []string{"one two", "three", "four"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("wrap = %q, want %q", got, want)
	}
	if got := wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Fatalf("wrap of empty = %q", got)
	}
}

func TestParseTopLevel(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("belai", flag.ContinueOnError)
		fs.String("prompt", "", "p")
		return fs
	}
	for _, help := range []string{"-help", "-h", "--help"} {
		var out, errb bytes.Buffer
		ok, code := parseTopLevel(newFS(), []string{help}, &out, &errb)
		if ok || code != 0 {
			t.Errorf("%s: ok=%v code=%d, want exit 0", help, ok, code)
		}
		if !strings.Contains(out.String(), "usage: belai") || errb.Len() != 0 {
			t.Errorf("%s: help must go to stdout alone; stdout=%q stderr=%q", help, out.String(), errb.String())
		}
	}

	var out, errb bytes.Buffer
	ok, code := parseTopLevel(newFS(), []string{"-nope"}, &out, &errb)
	if ok || code != 2 {
		t.Fatalf("bad flag: ok=%v code=%d, want exit 2", ok, code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "-nope") || !strings.Contains(errb.String(), "belai -help") {
		t.Errorf("bad flag: stdout=%q stderr=%q", out.String(), errb.String())
	}

	ok, code = parseTopLevel(newFS(), []string{"-prompt", "hi"}, io.Discard, io.Discard)
	if !ok || code != 0 {
		t.Fatalf("good flags: ok=%v code=%d", ok, code)
	}
}

func TestHelpTarget(t *testing.T) {
	got, ok := helpTarget([]string{"agent"})
	if !ok || strings.Join(got, " ") != "agent -h" {
		t.Fatalf("helpTarget(agent) = %q, %v", got, ok)
	}
	if _, ok := helpTarget([]string{"rc-session"}); ok {
		t.Fatal("a hidden command must not be reachable through help")
	}
	if _, ok := helpTarget([]string{"nope"}); ok {
		t.Fatal("an unknown word is not a command")
	}
}
