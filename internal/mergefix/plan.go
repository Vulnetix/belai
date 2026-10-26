package mergefix

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Strategy is the harness's suggestion for one attempt: a choice for every
// hunk, the fingerprint a resolution that follows it exactly would have, and a
// name made of choices and cleaned file names. A free strategy (every planned
// variation was tried) names no choices: the agent builds its own mix, and the
// harness still refuses a repeat.
type Strategy struct {
	Name        string
	Choices     []HunkResult
	Fingerprint string
	Free        bool
}

// FreeName is the free strategy's name.
const FreeName = "a mix of your own, different from every earlier attempt"

// assign is a choice per hunk key, with a default for the rest.
type assign struct {
	name   string
	def    Choice
	byFile map[string]Choice
	byHunk map[string]Choice
}

func (a assign) choice(file string, hunk int) Choice {
	if c, ok := a.byHunk[file+"#"+fmt.Sprint(hunk)]; ok {
		return c
	}
	if c, ok := a.byFile[file]; ok {
		return c
	}
	return a.def
}

// predict is what following a would classify as, hunk by hunk.
func predict(files []File, a assign) []HunkResult {
	var out []HunkResult
	for _, f := range files {
		if f.Whole {
			out = append(out, HunkResult{File: f.Path, Hunk: 0, Choice: Canonical(f, Hunk{}, a.choice(f.Path, 0))})
			continue
		}
		for _, h := range f.Hunks {
			out = append(out, HunkResult{File: f.Path, Hunk: h.Index, Choice: Canonical(f, h, a.choice(f.Path, h.Index))})
		}
	}
	return out
}

// preferenceOrder puts prefer first, then the other uniform choices in their
// fixed order.
func preferenceOrder(prefer Choice) []Choice {
	if !slices.Contains(Uniform, prefer) {
		prefer = Both
	}
	out := []Choice{prefer}
	for _, c := range Uniform {
		if c != prefer {
			out = append(out, c)
		}
	}
	return out
}

// maxPlanHunks bounds the per-hunk variations the planner tries.
const maxPlanHunks = 64

// listNames names up to three files, then how many more.
func listNames(names []string) string {
	shown := make([]string, 0, 3)
	for _, n := range names {
		if len(shown) == 3 {
			break
		}
		shown = append(shown, NoteFile(n))
	}
	s := strings.Join(shown, ", ")
	if rest := len(names) - len(shown); rest > 0 {
		s += fmt.Sprintf(" and %d more", rest)
	}
	return s
}

// candidates lists the variations in the order the loop tries them:
//
//  1. the preferred choice everywhere (accept both, current first, by default);
//  2. the next uniform choice everywhere (both, incoming first, by default);
//  3. for the files the last failed check named: current there, then incoming,
//     then both in the other order, with the preference elsewhere;
//  4. the remaining uniform choices everywhere (current, then incoming);
//  5. one file at a time, named files first, each other choice there;
//  6. one hunk at a time, each other choice there;
//  7. the named files keeping the preference and every other file current,
//     then incoming.
func candidates(files []File, implicated []string, prefer Choice) []assign {
	order := preferenceOrder(prefer)
	def := order[0]
	var out []assign
	uniform := func(c Choice) assign { return assign{name: string(c) + " everywhere", def: c} }
	out = append(out, uniform(order[0]), uniform(order[1]))
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	var named []string
	for _, p := range implicated {
		if slices.Contains(paths, p) && !slices.Contains(named, p) {
			named = append(named, p)
		}
	}
	sort.Strings(named)
	if len(named) > 0 {
		for _, c := range []Choice{Current, Incoming, BothIncomingFirst, Both} {
			if c == def {
				continue
			}
			by := map[string]Choice{}
			for _, p := range named {
				by[p] = c
			}
			out = append(out, assign{name: fmt.Sprintf("%s in %s, %s elsewhere", c, listNames(named), def), def: def, byFile: by})
		}
	}
	for _, c := range order[2:] {
		out = append(out, uniform(c))
	}
	ordered := append(slices.Clone(named), slices.DeleteFunc(slices.Clone(paths), func(p string) bool { return slices.Contains(named, p) })...)
	for _, p := range ordered {
		for _, c := range order[1:] {
			out = append(out, assign{name: fmt.Sprintf("%s in %s, %s elsewhere", c, NoteFile(p), def), def: def, byFile: map[string]Choice{p: c}})
		}
	}
	seen := 0
	for _, f := range files {
		if f.Whole || len(f.Hunks) < 2 {
			continue
		}
		for _, h := range f.Hunks {
			if seen >= maxPlanHunks {
				break
			}
			seen++
			key := f.Path + "#" + fmt.Sprint(h.Index)
			for _, c := range order[1:] {
				out = append(out, assign{name: fmt.Sprintf("%s in %s hunk %d, %s elsewhere", c, NoteFile(f.Path), h.Index+1, def), def: def, byHunk: map[string]Choice{key: c}})
			}
		}
	}
	if len(named) > 0 && len(named) < len(paths) {
		for _, c := range []Choice{Current, Incoming} {
			by := map[string]Choice{}
			for _, p := range named {
				by[p] = def
			}
			out = append(out, assign{name: fmt.Sprintf("%s in %s, %s elsewhere", def, listNames(named), c), def: c, byFile: by})
		}
	}
	return out
}

// Plan picks the strategy for the next attempt: the first variation, in the
// order candidates lists them, whose predicted fingerprint is not in tried.
// Variations that would produce the same resolution as one already listed are
// the same variation and are skipped. With none left it returns the free
// strategy.
func Plan(files []File, tried map[string]bool, implicated []string, prefer Choice) Strategy {
	seen := map[string]bool{}
	for _, a := range candidates(files, implicated, prefer) {
		pred := predict(files, a)
		fp := Fingerprint(pred)
		if seen[fp] || tried[fp] {
			seen[fp] = true
			continue
		}
		return Strategy{Name: a.name, Choices: pred, Fingerprint: fp}
	}
	return Strategy{Name: FreeName, Free: true}
}
