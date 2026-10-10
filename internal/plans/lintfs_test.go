package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a small repository: a root with src/a.ts and a sub-package
// "worker" that has its own package.json with a test script.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, body string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.ts", "x")
	write("README.md", "x")
	write("worker/package.json", `{"scripts":{"test":"vitest run","typecheck":"tsc --noEmit"}}`)
	write("worker/test/old.test.ts", "x")
	write("worker/src/b.ts", "x")
	return root
}

func plan(steps string) string {
	return "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n" + steps
}

func TestLintAtChecksPathsAgainstTheRepository(t *testing.T) {
	root := repo(t)
	cases := []struct {
		name  string
		steps string
		want  string // substring of an issue, "" for none
	}{
		{"real file", "1. A\n   - Files: `src/a.ts`\n   - Verify: none\n", ""},
		{"new file marked", "1. A\n   - Files: `docs/new.md` (new)\n   - Verify: none\n", ""},
		{"invented file", "1. A\n   - Files: `.repo/pix/launch-flow.md`\n   - Verify: none\n", ".repo/pix/launch-flow.md, which does not exist"},
		{"file made by an earlier new step", "1. A\n   - Files: `src/c.ts` (new)\n   - Verify: none\n2. B\n   - Files: `src/c.ts`\n   - Verify: none\n", ""},
		{"glob and prose are not checked", "1. A\n   - Files: `src/**/*.ts`, the worker\n   - Verify: none\n", ""},
		{"cd into a real dir", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm test`\n", ""},
		{"cd into a missing dir", "1. A\n   - Files: `src/a.ts`\n   - Verify: `cd sandbox && npm test`\n", "changes into sandbox, which does not exist"},
		{"npm at a root with no package.json", "1. A\n   - Files: `src/a.ts`\n   - Verify: `npm test`\n", "no package.json where it runs"},
		{"npm script not defined", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm run lint`\n", `script "lint"`},
		{"npm script defined", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm run typecheck`\n", ""},
		{"test path as a flag", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm test --test/old.test.ts`\n", "write npm test -- <path>"},
		{"existing test path", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm test -- test/old.test.ts`\n", ""},
		{"test path nowhere", "1. A\n   - Files: `worker/src/b.ts`\n   - Verify: `cd worker && npm test -- test/ghost.test.ts`\n", "test/ghost.test.ts, which does not exist and no step creates"},
		{"test made by this step", "1. A\n   - Files: `worker/test/new.test.ts` (new)\n   - Verify: `cd worker && npm test -- test/new.test.ts`\n", ""},
		{"go package missing", "1. A\n   - Files: `src/a.ts`\n   - Verify: `go test ./store`\n", "Go package ./store"},
	}
	for _, c := range cases {
		got := strings.Join(LintAt(plan(c.steps), root), "\n")
		if c.want == "" && got != "" {
			t.Errorf("%s: flagged %q", c.name, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: want an issue containing %q, got %q", c.name, c.want, got)
		}
	}
	// With no roots the rules do not run.
	if got := LintAt(plan("1. A\n   - Files: `.repo/ghost.md`\n   - Verify: `cd nowhere && npm test`\n")); len(got) != 0 {
		t.Errorf("no roots must mean no filesystem rules: %v", got)
	}
}

// A malformed package.json is not evidence of a missing script.
func TestLintAtToleratesAMalformedPackageJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LintAt(plan("1. A\n   - Files: `package.json`\n   - Verify: `npm run build`\n"), root)
	if len(got) != 0 {
		t.Fatalf("flagged %v", got)
	}
}
