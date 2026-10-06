package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// caseWords returns the string literals of the case clauses of the `switch cmd`
// in the function (or method) named fn in file: the verbs it dispatches.
func caseWords(t *testing.T, file, fn string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			// Only the switch on the verb itself: a nested switch (on a project
			// name, say) holds values, not commands.
			if tag, ok := sw.Tag.(*ast.Ident); !ok || tag.Name != "cmd" {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil && s != "" {
							seen[s] = true
						}
					}
				}
			}
			return true
		})
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// topLevelCommands returns the words main compares os.Args[1] against.
func topLevelCommands(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || be.Op != token.EQL {
			return true
		}
		idx, ok := be.X.(*ast.IndexExpr)
		if !ok {
			return true
		}
		sel, ok := idx.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Args" {
			return true
		}
		if lit, ok := be.Y.(*ast.BasicLit); ok && lit.Kind == token.STRING {
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

// TestEverySubcommandIsDocumented keeps the documentation in step with the
// command line: each top-level command, and each verb of agent, kanban and
// plugin, appears as `belai <command> [verb]` under docs/ or in the README.
func TestEverySubcommandIsDocumented(t *testing.T) {
	docs := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")

	top := topLevelCommands(t)
	if len(top) < 6 {
		t.Fatalf("only %d top-level commands found: %v", len(top), top)
	}
	for _, c := range top {
		if !strings.Contains(docs, "belai "+c) {
			t.Errorf("the command `belai %s` is not documented under docs/ or in the README", c)
		}
	}

	verbs := map[string][]string{
		"agent":  caseWords(t, "agentcmd.go", "agentCommand"),
		"kanban": caseWords(t, "kanbancmd.go", "run"),
		"plugin": caseWords(t, "plugin.go", "runPluginCLI"),
	}
	for cmd, vs := range verbs {
		if len(vs) < 4 {
			t.Fatalf("only %d verbs found for %s: %v", len(vs), cmd, vs)
		}
		for _, v := range vs {
			if !strings.Contains(docs, "belai "+cmd+" "+v) {
				t.Errorf("the command `belai %s %s` is not documented under docs/ or in the README", cmd, v)
			}
		}
	}
}
