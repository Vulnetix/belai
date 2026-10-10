package plans

import (
	"fmt"
	"regexp"
	"strings"
)

// Lint checks a plan document for the flaws that make a plan unusable to
// whoever carries it out, found by measuring planner output
// (docs/plan-mode-tuning.md): a step with nothing to run, a step whose Verify
// runs a test a later step creates, tests in the Test Plan that no step
// writes, a Verify that offers alternatives, and broken markup in a step
// heading. It is pure and deterministic, reads only the document, and returns
// one short sentence per issue, at most maxLintIssues, in document order. A
// plan that does not parse has no issues here: ParseDoc already rejects it.
//
// The sentences are fixed wording plus step numbers and paths taken from the
// plan itself, so a caller must treat them as plain text, never as a sealed
// harness block.
func Lint(md string) []string { return LintAt(md) }

// LintAt is Lint plus the filesystem rules (lintfs.go), which check the plan's
// claims against the repository under roots. With no roots it is Lint.
func LintAt(md string, roots ...string) []string {
	doc, err := ParseDoc(md)
	if err != nil {
		return nil
	}
	var issues []string
	add := func(s string) {
		if len(issues) < maxLintIssues {
			issues = append(issues, s)
		}
	}

	// firstNew[path] is the first step that lists the path as a new file;
	// listed[path] is the first step that lists it at all.
	firstNew := map[string]int{}
	listed := map[string]int{}
	testScheduled := false
	for i, s := range doc.Steps {
		for _, f := range s.Files {
			p, isNew := filePath(f)
			if p == "" {
				continue
			}
			if _, ok := listed[p]; !ok {
				listed[p] = i
			}
			if _, ok := firstNew[p]; !ok && isNew {
				firstNew[p] = i
			}
			if testPathRe.MatchString(p) {
				testScheduled = true
			}
		}
	}

	for i, s := range doc.Steps {
		n := s.N
		if strings.Count(s.Text, "**")%2 == 1 {
			add(fmt.Sprintf("step %d: unbalanced ** in its heading", n))
		}
		if len(s.Files) == 0 {
			add(fmt.Sprintf("step %d has no Files: line", n))
		}
		if strings.TrimSpace(s.Verify) == "" {
			add(fmt.Sprintf("step %d has no Verify: line (write \"Verify: none\" for a documentation-only step)", n))
			continue
		}
		if hedgedVerifyRe.MatchString(s.Verify) {
			add(fmt.Sprintf("step %d: Verify offers alternatives or conditions; give exactly one command", n))
		}
		if chainedVerify(s.Verify) {
			add(fmt.Sprintf("step %d: Verify chains several commands (&&, ||, ;); give exactly one command", n))
		}
		if len(roots) > 0 {
			for _, is := range fsIssues(roots, s, i, firstNew) {
				add(is)
			}
		}
		for _, tok := range verifyPaths(s.Verify) {
			cands := verifyCandidates(s.Verify, tok)
			j := firstNewOf(firstNew, cands)
			if j < 0 || j <= i {
				continue
			}
			// A test the step lists itself, or that an earlier step lists,
			// exists by now.
			if first := listedOf(listed, cands); first >= 0 && first <= i {
				continue
			}
			add(fmt.Sprintf("step %d: Verify runs %s, which step %d creates; put that test in step %d's Files or verify with a command that already works", n, tok, doc.Steps[j].N, n))
		}
	}

	if d := deliberation(md); d != "" {
		add(fmt.Sprintf("the plan contains deliberation (%q); write only the final decision, with no abandoned approaches", d))
	}
	if !testScheduled && listsNewTests(doc.Tests) {
		add("the Test Plan names new tests but no step has a test file in its Files; add the test file to the step that writes it, or take the new tests out of the Test Plan")
	}
	return issues
}

// maxLintIssues bounds how many issues one rejection carries, so the message
// stays short enough to act on.
const maxLintIssues = 6

var (
	// hedgedVerifyRe matches a Verify that offers a second command or a
	// condition instead of one command.
	hedgedVerifyRe = regexp.MustCompile("(?i)(\\bor\\s+`|\\(or\\s|\\bif (it|that|the|there)\\b[^`]*(exist|available|present))")

	// testPathRe matches a path that is a test file in the common layouts.
	testPathRe = regexp.MustCompile(`(?i)(^|/)(tests?|__tests__|spec)/|_test\.go$|\.(test|spec)\.[cm]?[jt]sx?$|(^|/)test_[^/]+\.py$|_test\.py$`)

	backtickRe = regexp.MustCompile("`([^`]+)`")
)

// filePath extracts the path from a Files entry such as "`src/a.ts` (new)" and
// says whether the entry marks it as a new file.
func filePath(entry string) (path string, isNew bool) {
	isNew = strings.Contains(strings.ToLower(entry), "(new)")
	if m := backtickRe.FindStringSubmatch(entry); m != nil {
		return strings.TrimSpace(m[1]), isNew
	}
	f := strings.Fields(entry)
	if len(f) == 0 {
		return "", isNew
	}
	return strings.Trim(f[0], "`'\",;"), isNew
}

// verifyPaths returns the test-file paths a Verify command names, without any
// "::test_name" or ":line" suffix.
func verifyPaths(verify string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.Fields(strings.NewReplacer("`", " ", "'", " ", "\"", " ").Replace(verify)) {
		if i := strings.Index(tok, "::"); i >= 0 {
			tok = tok[:i]
		}
		tok = strings.TrimRight(tok, ".,;)")
		tok = strings.TrimPrefix(tok, "./")
		if tok == "" || !strings.ContainsAny(tok, "/.") || !testPathRe.MatchString(tok) || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

var (
	// testNameRe matches an identifier that names a test case: a Go test or a
	// Python test function.
	testNameRe = regexp.MustCompile(`\bTest[A-Z]\w*|\btest_\w+`)
	// caseVerbRe matches a bullet that states what a case asserts.
	caseVerbRe = regexp.MustCompile(`(?i)\b(asserts?|expects?|returns?|rejects?|responds?|creates?|fails?)\b`)
	// existingRe matches a bullet about the suite that is already there.
	existingRe = regexp.MustCompile(`(?i)\b(existing|still|already)\b`)
)

// listsNewTests reports whether a Test Plan names test cases to write, as
// opposed to saying the existing suite is run. A bullet counts when it carries
// a test identifier, or a "name — what it asserts" shape that is not about the
// existing suite. A plan that only runs what is already there has nothing for
// a step to schedule, and asking it for a test file would only make it invent
// one.
func listsNewTests(tests []string) bool {
	for _, t := range tests {
		if testNameRe.MatchString(t) {
			return true
		}
		if strings.Contains(t, "—") && caseVerbRe.MatchString(t) && !existingRe.MatchString(t) {
			return true
		}
	}
	return false
}

// Paths returns the file paths the plan's steps list, in order and without
// duplicates, at most maxPlanPaths. A Files entry such as "`src/a.ts` (new): the
// engine" yields "src/a.ts". It is the harness's own reading of what the user
// approved to change, not model prose.
func (d Doc) Paths() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range d.Steps {
		for _, f := range s.Files {
			p, _ := filePath(f)
			p = strings.TrimSuffix(strings.TrimSpace(p), ":")
			if p == "" || seen[p] || len(out) >= maxPlanPaths {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// maxPlanPaths bounds how many paths Doc.Paths returns.
const maxPlanPaths = 100

var (
	// deliberationRe matches the thinking-out-loud phrases a planner leaks into
	// plan text: "Wait...", "Re-evaluate", "Simpler final approach", "Scratch
	// that". A plan states its final decision, not the route to it.
	deliberationRe = regexp.MustCompile(`(?i)\bwait[.,…!]|\bhmm\b|\bon second thought\b|\bre-?evaluat|\bscratch that\b|\bsimpler (final )?approach\b|\blet me (re)?think\b|\bactually,|\bnever ?mind\b`)

	// strayScriptRe matches letters from scripts a model sometimes drops into an
	// English sentence by accident.
	strayScriptRe = regexp.MustCompile(`[\p{Han}\p{Hangul}\p{Hiragana}\p{Katakana}\p{Cyrillic}\p{Arabic}\p{Hebrew}\p{Thai}]+`)
)

// deliberation returns the first deliberation phrase outside code fences, or "".
func deliberation(md string) string {
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence {
			if m := deliberationRe.FindString(line); m != "" {
				return strings.TrimSpace(m)
			}
		}
	}
	return ""
}

// StrayScript returns the first run of letters from an unexpected script (CJK,
// Hangul, Cyrillic, Arabic, Hebrew, Thai) in text, or "". The caller compares it
// with the request: a plan for a request written in that script may use it.
func StrayScript(text string) string {
	return strayScriptRe.FindString(text)
}

// chainedVerify reports whether a Verify strings several commands together.
// One leading "cd <dir> &&" is allowed: it says where the one command runs.
// Operators inside quotes (a grep pattern) do not count.
func chainedVerify(verify string) bool {
	v := strings.Trim(strings.TrimSpace(verify), "` ")
	if m := cdRe.FindStringIndex(v); m != nil && m[0] <= 1 {
		v = strings.TrimSpace(v[m[1]:])
	}
	var quote rune
	rs := []rune(v)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ';':
			return true
		case (c == '&' || c == '|') && i+1 < len(rs) && rs[i+1] == c:
			return true
		}
	}
	return false
}
