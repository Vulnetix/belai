package plans

import (
	"strings"
	"testing"
)

const cleanPlan = "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Add Patch\n   - Files: `store/store.go`, `store/store_test.go`\n   - Verify: `go test ./store`\n2. Route it\n   - Files: `main.go`\n   - Verify: `go build ./...`\n\n## Test Plan\n\n- TestPatch — x\n"

func TestLintAcceptsACleanPlan(t *testing.T) {
	if got := Lint(cleanPlan); len(got) != 0 {
		t.Fatalf("clean plan flagged: %v", got)
	}
	if got := Lint("not a plan"); got != nil {
		t.Fatalf("a document that does not parse has no lint issues, got %v", got)
	}
}

func TestLintNamesEachFlaw(t *testing.T) {
	cases := []struct {
		name string
		plan string
		want string
	}{
		{"no files", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Verify: go build\n", "step 1 has no Files"},
		{"no verify", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: a.go\n", "step 1 has no Verify"},
		{"unbalanced bold", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. **Add the handler\n   - Files: a.go\n   - Verify: go build\n", "unbalanced **"},
		{"hedged verify", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: a.go\n   - Verify: `go test ./a` or `go build ./...`\n", "offers alternatives"},
		{"conditional verify", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: a.go\n   - Verify: go test ./a (if it does not exist yet, go build)\n", "offers alternatives"},
		{"test created later", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code\n   - Files: `src/a.ts`\n   - Verify: `npx vitest run test/a.test.ts`\n2. Tests\n   - Files: `test/a.test.ts` (new)\n   - Verify: `npx vitest run test/a.test.ts`\n", "step 1: Verify runs test/a.test.ts, which step 2 creates"},
		{"python node id", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code\n   - Files: `app.py`\n   - Verify: `pytest tests/test_app.py::test_x`\n2. Tests\n   - Files: `tests/test_app.py` (new)\n   - Verify: `pytest tests/test_app.py`\n", "which step 2 creates"},
		{"tests unscheduled", "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code\n   - Files: `a.go`\n   - Verify: go build\n\n## Test Plan\n\n- TestA — x\n", "no step has a test file"},
	}
	for _, c := range cases {
		got := strings.Join(Lint(c.plan), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want an issue containing %q, got %q", c.name, c.want, got)
		}
	}
}

// A test the step lists itself, an existing test file edited later, and a
// documentation step that says none are all fine.
func TestLintDoesNotFlagWhatWorks(t *testing.T) {
	for name, plan := range map[string]string{
		"step creates its own test":  "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code and test\n   - Files: `src/a.ts`, `test/a.test.ts` (new)\n   - Verify: `npx vitest run test/a.test.ts`\n",
		"existing test edited later": "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Code\n   - Files: `a.go`\n   - Verify: `go test ./...`\n2. More tests\n   - Files: `a_test.go`\n   - Verify: `go test ./...`\n\n## Test Plan\n\n- TestA — x\n",
		"docs step":                  "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Docs\n   - Files: `README.md`\n   - Verify: none\n",
		"or inside a command":        "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Docs\n   - Files: `README.md`\n   - Verify: `grep -q 'a\\|b' README.md`\n",
	} {
		if got := Lint(plan); len(got) != 0 {
			t.Errorf("%s flagged: %v", name, got)
		}
	}
}

func TestLintIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("# T\n\n## Summary\n\nx\n\n## Key Changes\n\n")
	for i := 1; i <= 12; i++ {
		b.WriteString("1. step\n")
	}
	if got := Lint(b.String()); len(got) != maxLintIssues {
		t.Fatalf("issues = %d, want the cap %d", len(got), maxLintIssues)
	}
}

// A Test Plan that only says the existing suite runs names nothing for a step
// to write; asking such a plan for a test file made one invent it.
func TestLintLeavesAnExistingSuiteTestPlanAlone(t *testing.T) {
	base := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Change the port\n   - Files: `main.go`\n   - Verify: `go build ./...`\n\n## Test Plan\n\n"
	for name, tests := range map[string]string{
		"existing suite":      "- Existing `go test ./...` — ensures the package still builds and tests pass after the port string change.\n",
		"run what is there":   "- Run `go test ./...`.\n",
		"existing with verbs": "- The existing tests — still return the same results; run `npm test`.\n",
	} {
		if got := Lint(base + tests); len(got) != 0 {
			t.Errorf("%s was flagged: %v", name, got)
		}
	}
	for name, tests := range map[string]string{
		"go name":     "- `TestPatch` — updates only the title.\n",
		"python name": "- `test_pagination` — returns 20 rows.\n",
		"case shape":  "- POST /tasks with a missing title — returns 400.\n",
	} {
		if got := Lint(base + tests); len(got) != 1 || !strings.Contains(got[0], "names new tests") {
			t.Errorf("%s should be flagged once: %v", name, got)
		}
	}
}

func TestDocPathsReadsFilesEntries(t *testing.T) {
	d, err := ParseDoc("# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `src/a.ts` (new), test/a.test.ts\n   - Verify: none\n2. B\n   - Files:\n     - `src/b.ts`: wire it, with commas\n     - src/a.ts\n   - Verify: none\n")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(d.Paths(), "|")
	if got != "src/a.ts|test/a.test.ts|src/b.ts" {
		t.Fatalf("Paths = %q", got)
	}
}

func TestLintFlagsChainedVerifyButNotACdPrefixOrQuotedOperators(t *testing.T) {
	step := func(v string) string {
		return "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. A\n   - Files: `a.go`\n   - Verify: " + v + "\n"
	}
	for _, v := range []string{"`go build ./... && go test ./...`", "`npm run build; npm test`", "`make a || make b`", "`cd w && npm test && npm run lint`"} {
		if got := strings.Join(Lint(step(v)), "\n"); !strings.Contains(got, "chains several commands") {
			t.Errorf("%s: not flagged: %q", v, got)
		}
	}
	for _, v := range []string{"`go test ./store`", "`cd worker && npm test -- test/a.test.ts`", "`grep -q 'a&&b' README.md`", "`grep -E \"x;y\" README.md`", "`go test ./... | tail -3`"} {
		if got := Lint(step(v)); len(got) != 0 {
			t.Errorf("%s: flagged %v", v, got)
		}
	}
}

func TestLintFlagsLeakedDeliberation(t *testing.T) {
	leaky := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Wire it\n   - Add the binding. Wait... re-evaluate: a simpler final approach is a Map.\n   - Files: `a.go`\n   - Verify: none\n"
	if got := strings.Join(Lint(leaky), "\n"); !strings.Contains(got, "contains deliberation") {
		t.Fatalf("not flagged: %q", got)
	}
	fenced := "# T\n\n## Summary\n\nx\n\n## Key Changes\n\n1. Wire it\n   - Files: `a.go`\n   - Verify: none\n\n```\nWait... this is a code sample\n```\n"
	if got := Lint(fenced); len(got) != 0 {
		t.Fatalf("text in a code fence was flagged: %v", got)
	}
}

func TestStrayScript(t *testing.T) {
	if StrayScript("limited to the route and its 테스트.") != "테스트" {
		t.Fatal("Hangul not found")
	}
	if StrayScript("an ordinary English plan, café included") != "" {
		t.Fatal("Latin accents are not stray")
	}
}
