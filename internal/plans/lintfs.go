package plans

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The filesystem rules of the plan lint. They check claims a plan makes about
// the repository against the repository itself, read-only: a file that does not
// exist, a directory a Verify changes into, an npm script that is not defined,
// a test path that is nowhere. Measured plans for a large repository lost most
// of their points on exactly these (docs/plan-mode-tuning.md): they invented a
// docs directory, ran sub-package tests from the repository root, and wrote
// `npm test --path` for `npm test -- path`.
//
// Paths are looked up under the roots the caller gives (the working directory
// and any added workspace roots). With no roots the rules do not run, so Lint
// stays pure.

var (
	cdRe        = regexp.MustCompile("(?:^|[\\s`&;(])cd\\s+(\\S+?)\\s*(?:&&|;)")
	npmScriptRe = regexp.MustCompile(`\bnpm\s+(?:--prefix\s+\S+\s+)?(run(?:-script)?\s+(\S+)|test|t)\b`)
	npmFlagPath = regexp.MustCompile(`\bnpm\s+(?:run\s+)?test\s+--[^\s-]`)
	goPkgRe     = regexp.MustCompile(`\bgo\s+(?:test|build|vet)\s+(\./[A-Za-z0-9_./-]+)`)
)

// fsIssues returns the issues for one step. firstNewIdx maps a path to the
// first step that lists it as new; i is this step's index.
func fsIssues(roots []string, s Step, i int, firstNew map[string]int) []string {
	var out []string

	// Files that must already exist.
	missing := 0
	for _, f := range s.Files {
		p, isNew := filePath(f)
		p = strings.TrimSuffix(strings.TrimSpace(p), ":")
		if isNew || !pathLike(p) || existsUnder(roots, p) {
			continue
		}
		// Created by an earlier step that marked it new: fine.
		if j, ok := firstNew[p]; ok && j <= i {
			continue
		}
		if missing++; missing <= 2 {
			out = append(out, fmt.Sprintf("step %d lists %s, which does not exist; mark it (new) if the step creates it, or use the real path", s.N, p))
		}
	}

	v := strings.Trim(strings.TrimSpace(s.Verify), "` ")
	if v == "" || strings.EqualFold(v, "none") {
		return out
	}

	// Where the command runs.
	bases := roots
	if m := cdRe.FindStringSubmatch(v); m != nil {
		dir := strings.Trim(m[1], "`'\"")
		var found []string
		for _, r := range roots {
			if st, err := os.Stat(filepath.Join(r, dir)); err == nil && st.IsDir() {
				found = append(found, filepath.Join(r, dir))
			}
		}
		if len(found) == 0 {
			// A directory an earlier step creates is fine.
			if j, ok := firstNew[dir]; !ok || j > i {
				out = append(out, fmt.Sprintf("step %d: Verify changes into %s, which does not exist", s.N, dir))
			}
			return out
		}
		bases = found
	}

	// npm: a package.json that defines the script.
	if m := npmScriptRe.FindStringSubmatch(v); m != nil {
		script := "test"
		if strings.HasPrefix(m[1], "run") {
			script = m[2]
		}
		if npmFlagPath.MatchString(v) {
			out = append(out, fmt.Sprintf("step %d: Verify passes a test path as a flag; write npm test -- <path> with a separate --", s.N))
		}
		if !strings.Contains(v, "--prefix") {
			switch has, def := npmDefines(bases, script); {
			case !has:
				out = append(out, fmt.Sprintf("step %d: Verify uses npm but there is no package.json where it runs; run it from the directory that has one (cd <dir> && npm …)", s.N))
			case !def:
				out = append(out, fmt.Sprintf("step %d: Verify runs npm script %q, which package.json does not define", s.N, script))
			}
		}
	}

	// Test files the command names must exist or be created by this step or an earlier one.
	for _, tok := range verifyPaths(v) {
		if existsUnder(bases, tok) || existsUnder(roots, tok) {
			continue
		}
		if j := firstNewOf(firstNew, verifyCandidates(v, tok)); j >= 0 {
			continue // created by this or an earlier step, or by a later one the order rule reports
		}
		out = append(out, fmt.Sprintf("step %d: Verify runs %s, which does not exist and no step creates", s.N, tok))
		break
	}

	// Go packages the command names.
	if m := goPkgRe.FindStringSubmatch(v); m != nil && !strings.Contains(m[1], "...") {
		if !existsUnder(bases, strings.TrimPrefix(m[1], "./")) && !existsUnder(roots, strings.TrimPrefix(m[1], "./")) {
			if j, ok := firstNew[strings.TrimPrefix(m[1], "./")]; !ok || j > i {
				out = append(out, fmt.Sprintf("step %d: Verify names the Go package %s, which does not exist", s.N, m[1]))
			}
		}
	}
	return out
}

// pathLike reports whether a Files entry names a path the check can test: not
// empty, not a glob, not a URL, and shaped like a file or directory path.
func pathLike(p string) bool {
	if p == "" || strings.ContainsAny(p, "*{}<>|") || strings.Contains(p, "://") || strings.ContainsAny(p, " \t") {
		return false
	}
	return strings.Contains(p, "/") || strings.Contains(p, ".")
}

// existsUnder reports whether p exists under any root (or is absolute and exists).
func existsUnder(roots []string, p string) bool {
	if filepath.IsAbs(p) {
		_, err := os.Stat(p)
		return err == nil
	}
	for _, r := range roots {
		if _, err := os.Stat(filepath.Join(r, p)); err == nil {
			return true
		}
	}
	return false
}

// npmDefines reports whether a package.json exists in any base and whether one
// defines the script. An unreadable or malformed package.json counts as present
// and defining, so only a certain absence is reported.
func npmDefines(bases []string, script string) (has, defines bool) {
	for _, b := range bases {
		data, err := os.ReadFile(filepath.Join(b, "package.json"))
		if err != nil {
			continue
		}
		has = true
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			return true, true
		}
		if _, ok := pkg.Scripts[script]; ok {
			return true, true
		}
	}
	return has, false
}

// verifyCandidates returns the repository-relative spellings of a path a Verify
// names: as written, and under the directory the command changes into
// ("cd worker && npm test -- test/a.test.ts" names worker/test/a.test.ts).
func verifyCandidates(verify, tok string) []string {
	out := []string{tok}
	if m := cdRe.FindStringSubmatch(strings.Trim(strings.TrimSpace(verify), "` ")); m != nil {
		dir := strings.Trim(m[1], "`'\"")
		out = append(out, filepath.ToSlash(filepath.Join(dir, tok)))
	}
	return out
}

// firstNewOf is the first step, over the candidates, that lists one as a new
// file, or -1.
func firstNewOf(firstNew map[string]int, cands []string) int {
	best := -1
	for _, c := range cands {
		if j, ok := firstNew[c]; ok && (best < 0 || j < best) {
			best = j
		}
	}
	return best
}

// listedOf is the same for the first step that lists a candidate at all.
func listedOf(listed map[string]int, cands []string) int {
	best := -1
	for _, c := range cands {
		if j, ok := listed[c]; ok && (best < 0 || j < best) {
			best = j
		}
	}
	return best
}
