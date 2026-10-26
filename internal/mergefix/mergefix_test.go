package mergefix

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const conflicted = `package a

import (
<<<<<<< HEAD
	"fmt"
	"os"
||||||| base
	"fmt"
=======
	"fmt"
	"strings"
>>>>>>> main
)

func A() {
	x := 1
<<<<<<< HEAD
	y := 2
=======
	z := 3
>>>>>>> main
	_ = x
}
`

func mustParse(t *testing.T, path, s string) File {
	t.Helper()
	f, err := ParseFile(path, []byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseFileReadsDiff3Hunks(t *testing.T) {
	f := mustParse(t, "a.go", conflicted)
	if len(f.Hunks) != 2 || len(f.Common) != 3 {
		t.Fatalf("hunks %d common %d", len(f.Hunks), len(f.Common))
	}
	h := f.Hunks[0]
	if h.Line != 4 || !slices.Equal(h.Ours, []string{"\t\"fmt\"", "\t\"os\""}) || !slices.Equal(h.Base, []string{"\t\"fmt\""}) || !slices.Equal(h.Theirs, []string{"\t\"fmt\"", "\t\"strings\""}) {
		t.Fatalf("hunk 0 %+v", h)
	}
	if f.Hunks[1].Index != 1 || !slices.Equal(f.Hunks[1].Theirs, []string{"\tz := 3"}) {
		t.Fatalf("hunk 1 %+v", f.Hunks[1])
	}
}

func TestParseFileRefusesMalformedMarkers(t *testing.T) {
	for name, s := range map[string]string{
		"unterminated": "a\n<<<<<<< HEAD\nb\n=======\nc\n",
		"nested":       "<<<<<<< HEAD\n<<<<<<< x\n=======\n>>>>>>> y\n",
	} {
		if _, err := ParseFile("f", []byte(s)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// A separator line and a stray closer outside a hunk are text.
	f, err := ParseFile("README.md", []byte("Title\n=======\n>>>>>>> quoted\n"))
	if err != nil || len(f.Hunks) != 0 {
		t.Fatalf("plain text: %+v %v", f, err)
	}
}

func TestClassifyRegion(t *testing.T) {
	o, th := []string{"a", "b"}, []string{"c"}
	for _, tc := range []struct {
		name   string
		region []string
		want   Choice
	}{
		{"both current first", []string{"a", "b", "c"}, Both},
		{"both with a blank line and new indentation", []string{"  a", "b", "", "c"}, Both},
		{"both incoming first", []string{"c", "a", "b"}, BothIncomingFirst},
		{"current", []string{"a", "b"}, Current},
		{"incoming", []string{"c"}, Incoming},
		{"mixed", []string{"a", "c"}, Mixed},
		{"mostly sides", []string{"a", "c", "new"}, Mixed},
		{"other", []string{"new", "lines", "a"}, Other},
		{"empty", nil, Other},
	} {
		got, d := ClassifyRegion(tc.region, o, th)
		if got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
		if (got == Mixed || got == Other) == (d == "") {
			t.Errorf("%s: digest %q", tc.name, d)
		}
	}
}

func TestClassifyPlacesHunksInTheResolvedFile(t *testing.T) {
	f := mustParse(t, "a.go", conflicted)
	resolved := `package a

import (
	"fmt"
	"os"
	"fmt"
	"strings"
)

// a comment the agent added above the function
func A() {
	x := 1
	z := 3
	_ = x
}
`
	got := Classify(f, []byte(resolved), false)
	if len(got) != 2 || got[0].Choice != Both || got[1].Choice != Incoming {
		t.Fatalf("results %+v", got)
	}
	// Every hunk of a deleted file is other.
	for _, r := range Classify(f, nil, true) {
		if r.Choice != Other || r.Digest != "deleted" {
			t.Fatalf("deleted %+v", r)
		}
	}
	// Surroundings rewritten past recognition: the hunk is unplaced, not guessed.
	r := Classify(f, []byte("nothing in common\n"), false)
	if r[0].Choice != Other || r[0].Digest != "unplaced" {
		t.Fatalf("unplaced %+v", r)
	}
}

func TestClassifyWhole(t *testing.T) {
	for _, tc := range []struct {
		ours, theirs, result string
		want                 Choice
	}{
		{"aaaa", "bbbb", "aaaa", Current},
		{"aaaa", "bbbb", "bbbb", Incoming},
		{"aaaa", "", "", Incoming},
		{"", "bbbb", "", Current},
		{"aaaa", "bbbb", "cccc", Other},
	} {
		if got, _ := ClassifyWhole(tc.ours, tc.theirs, tc.result); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

func TestFingerprintIsStableAndOrderFree(t *testing.T) {
	a := []HunkResult{{File: "a.go", Hunk: 0, Choice: Both}, {File: "b.go", Hunk: 1, Choice: Current}}
	b := []HunkResult{a[1], a[0]}
	if Fingerprint(a) != Fingerprint(b) || len(Fingerprint(a)) != 8 {
		t.Fatalf("%s %s", Fingerprint(a), Fingerprint(b))
	}
	// Pinned: a change of the vector's encoding changes every recorded ledger.
	if got := Fingerprint(a); got != Fingerprint([]HunkResult{{File: "b.go", Hunk: 1, Choice: Current}, {File: "a.go", Hunk: 0, Choice: Both}}) {
		t.Fatal(got)
	}
	c := []HunkResult{{File: "a.go", Hunk: 0, Choice: BothIncomingFirst}, a[1]}
	if Fingerprint(c) == Fingerprint(a) {
		t.Fatal("the two orders of both share a fingerprint")
	}
	m1 := []HunkResult{{File: "a.go", Hunk: 0, Choice: Mixed, Digest: "11111111"}}
	m2 := []HunkResult{{File: "a.go", Hunk: 0, Choice: Mixed, Digest: "22222222"}}
	if Fingerprint(m1) == Fingerprint(m2) {
		t.Fatal("two different mixes share a fingerprint")
	}
}

func TestSummarise(t *testing.T) {
	got := Summarise([]HunkResult{
		{File: "b.go", Hunk: 0, Choice: Both}, {File: "b.go", Hunk: 1, Choice: BothIncomingFirst},
		{File: "a.go", Hunk: 0, Choice: Current}, {File: "a.go", Hunk: 1, Choice: Incoming},
	})
	want := []FileChoice{{"a.go", "mixed"}, {"b.go", "both"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

func twoFiles(t *testing.T) []File {
	return []File{
		mustParse(t, "a.go", conflicted),
		mustParse(t, "b.txt", "x\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> main\ny\n"),
	}
}

func TestPlanStartsWithAcceptBothAndNeverRepeats(t *testing.T) {
	files := twoFiles(t)
	first := Plan(files, nil, nil, Both)
	if first.Name != "both everywhere" || first.Free {
		t.Fatalf("first %+v", first)
	}
	tried := map[string]bool{}
	var names []string
	for i := 0; i < 200; i++ {
		s := Plan(files, tried, nil, Both)
		if s.Free {
			break
		}
		if tried[s.Fingerprint] {
			t.Fatalf("attempt %d repeats %s (%s)", i+1, s.Fingerprint, s.Name)
		}
		if got := Fingerprint(s.Choices); got != s.Fingerprint {
			t.Fatalf("predicted fingerprint %s for choices %s", s.Fingerprint, got)
		}
		tried[s.Fingerprint] = true
		names = append(names, s.Name)
	}
	if len(names) < 5 {
		t.Fatalf("only %d variations: %v", len(names), names)
	}
	if names[1] != "both-incoming-first everywhere" {
		t.Fatalf("second %q", names[1])
	}
	if s := Plan(files, tried, nil, Both); !s.Free || s.Name != FreeName {
		t.Fatalf("after every variation %+v", s)
	}
}

func TestPlanTurnsToTheImplicatedFiles(t *testing.T) {
	files := twoFiles(t)
	tried := map[string]bool{
		Plan(files, nil, nil, Both).Fingerprint: true,
	}
	tried[Plan(files, tried, nil, Both).Fingerprint] = true
	s := Plan(files, tried, []string{"a.go", "not-conflicted.go"}, Both)
	if s.Name != "current in a.go, both elsewhere" {
		t.Fatalf("%q", s.Name)
	}
	for _, c := range s.Choices {
		if want := map[string]Choice{"a.go": Current, "b.txt": Both}[c.File]; c.Choice != want {
			t.Fatalf("choice %+v", c)
		}
	}
}

func TestPlanHonoursThePreference(t *testing.T) {
	if s := Plan(twoFiles(t), nil, nil, Incoming); s.Name != "incoming everywhere" {
		t.Fatalf("%q", s.Name)
	}
}

func TestPlanSkipsEquivalentVariations(t *testing.T) {
	// The current side is empty, so both, current first, is the incoming side:
	// they are one variation, not two.
	f := mustParse(t, "c.txt", "<<<<<<< HEAD\n=======\nadded\n>>>>>>> main\n")
	tried := map[string]bool{}
	n := 0
	for {
		s := Plan([]File{f}, tried, nil, Both)
		if s.Free {
			break
		}
		tried[s.Fingerprint] = true
		n++
	}
	if n != 2 {
		t.Fatalf("%d variations of a one-sided hunk, want 2 (keep it, drop it)", n)
	}
}

func TestDuplicateImports(t *testing.T) {
	for _, tc := range []struct {
		path, src string
		want      []string
	}{
		{"a.go", "package a\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"fmt\"\n)\n", []string{"import fmt"}},
		{"a.go", "package a\n\nimport \"fmt\"\nimport f \"fmt\"\nimport f \"strings\"\n", []string{"name f"}},
		{"a.go", "package a\n\nimport (\n\t\"fmt\" // print\n\t_ \"embed\"\n)\n", nil},
		{"a.ts", "import { a, b } from './x';\nimport { b as c } from './y';\nimport {\n  a,\n} from './z';\n", []string{"name a"}},
		{"a.tsx", "import React from 'react';\nimport * as React from 'react';\nimport './side.css';\nimport './side.css';\n", []string{"module ./side.css", "name React"}},
		{"a.js", "import type { T } from './t';\nimport { type T, U } from './u';\n", []string{"name T"}},
		{"a.py", "import os\nimport os\nfrom x import (a,\n    b)\nfrom y import b as c\nfrom z import c\ntry:\n    import json\nexcept ImportError:\n    import simplejson as json\n", []string{"import os", "name c"}},
		{"a.py", "import os.path\nimport os\n", nil},
		{"lib.rs", "use std::collections::HashMap;\nuse std::{fmt, collections::{HashMap, HashSet}};\nuse std::io::Result as IoResult;\n\nmod tests {\n    use super::*;\n    use std::fmt;\n}\n", []string{"name HashMap"}},
		{"lib.rs", "pub use crate::a::{self, b as fmt};\nuse std::fmt;\n", []string{"name fmt"}},
		{"A.java", "import java.util.List;\nimport java.util.List;\nimport static org.junit.Assert.assertTrue;\n", []string{"import java.util.List"}},
		{"A.kt", "import a.B as C\nimport a.B as C\nimport a.B\n", []string{"import a.B as C"}},
		{"A.cs", "using System;\nglobal using System.Linq;\nusing System;\nusing (var s = Open()) {}\n", []string{"using System"}},
		{"README.md", "import os\nimport os\n", nil},
	} {
		if got := DuplicateImports(tc.path, []byte(tc.src)); !slices.Equal(got, tc.want) {
			t.Errorf("%s %q: %v, want %v", tc.path, tc.src, got, tc.want)
		}
	}
}

func TestNewDuplicateImportsIgnoresWhatASideHad(t *testing.T) {
	had := []byte("import os\nimport os\n")
	if got := NewDuplicateImports("a.py", []byte("import os\nimport os\nimport re\nimport re\n"), had, nil); !slices.Equal(got, []string{"import re"}) {
		t.Fatal(got)
	}
}

func TestMarkersLeft(t *testing.T) {
	if !MarkersLeft([]byte("a\n<<<<<<< HEAD\nb\n"), []byte("a\n"), []byte("b\n")) {
		t.Fatal("a marker the merge left was not seen")
	}
	// A file that documents the markers keeps its own.
	doc := []byte("Example:\n<<<<<<< HEAD\n>>>>>>> main\n")
	if MarkersLeft(doc, doc, []byte("x\n")) {
		t.Fatal("the side's own markers were counted")
	}
	if MarkerCount([]byte("Title\n=======\n")) != 0 {
		t.Fatal("a heading underline is a marker")
	}
}

func TestNoteFormatAndParse(t *testing.T) {
	a := Attempt{N: 2, Max: 5, Fingerprint: "1a2b3c4d", Files: []FileChoice{{"b.go", "current"}, {"a.go", "both"}}, Result: Result{Outcome: BuildFailed, Command: "go build ./..."}}
	note := FormatNote(a)
	if note != "conflict attempt 2/5 1a2b3c4d: a.go=both, b.go=current -> build failed (go build ./...)" {
		t.Fatalf("%q", note)
	}
	got, ok := ParseNote(note)
	if !ok || got.N != 2 || got.Max != 5 || got.Fingerprint != "1a2b3c4d" || got.Result != a.Result || len(got.Files) != 2 {
		t.Fatalf("%+v %v", got, ok)
	}
	for _, r := range []Result{
		{Outcome: Passed},
		{Outcome: LintFailed, Linter: "lint-golangci", Count: 3},
		{Outcome: TestsFailed, Suite: "go"},
		{Outcome: MarkersFound},
		{Outcome: DuplicateImportsFound, File: "a.go"},
		{Outcome: Repeat, RepeatOf: 1},
	} {
		a.Result = r
		back, ok := ParseNote(FormatNote(a))
		if !ok || back.Result != r {
			t.Errorf("%+v did not round-trip: %q %+v", r, FormatNote(a), back.Result)
		}
	}
	if _, ok := ParseNote("agent belai:x completed it"); ok {
		t.Fatal("parsed another note")
	}
}

func TestNoteListsTwelveFilesThenMore(t *testing.T) {
	var files []FileChoice
	for i := 0; i < 15; i++ {
		files = append(files, FileChoice{File: "pkg/f" + string(rune('a'+i)) + ".go", Choice: "both"})
	}
	note := FormatNote(Attempt{N: 1, Max: 5, Fingerprint: "00000000", Files: files, Result: Result{Outcome: Passed}})
	if strings.Count(note, "=both") != 12 || !strings.Contains(note, ", +3 more -> passed") {
		t.Fatalf("%q", note)
	}
	// A long list is cut further to stay inside the board's note limit.
	var long []FileChoice
	for i := 0; i < 12; i++ {
		long = append(long, FileChoice{File: strings.Repeat("d", 100) + "/" + string(rune('a'+i)), Choice: "current"})
	}
	if n := FormatNote(Attempt{N: 1, Max: 5, Fingerprint: "00000000", Files: long, Result: Result{Outcome: Passed}}); len(n) > maxNoteLen || !strings.Contains(n, "more") {
		t.Fatalf("%d %q", len(n), n)
	}
}

func TestNoteFileHidesUnsafeNames(t *testing.T) {
	for _, p := range []string{"a b.go", "x,y=z.go", "../up.go", "ünï.go", strings.Repeat("a", 121)} {
		if got := NoteFile(p); !strings.HasPrefix(got, "file-") || len(got) != 13 {
			t.Errorf("%q -> %q", p, got)
		}
	}
	if NoteFile("pkg/a_b-c.go") != "pkg/a_b-c.go" {
		t.Fatal("a plain path was renamed")
	}
}

func TestFinalNotesAndSeries(t *testing.T) {
	if FinalResolved(3, "0123456789abcdef") != "conflicts resolved on attempt 3, pushed 0123456789ab" {
		t.Fatal(FinalResolved(3, "0123456789abcdef"))
	}
	if FinalUnresolved(5) != "conflicts unresolved after 5 attempts" {
		t.Fatal(FinalUnresolved(5))
	}
	if RetryNote(2) != "conflict retry: round 2" {
		t.Fatal(RetryNote(2))
	}
	note := func(fp string) string {
		return FormatNote(Attempt{N: 1, Max: 5, Fingerprint: fp, Files: []FileChoice{{"a", "both"}}, Result: Result{Outcome: MarkersFound}})
	}
	// Round 1 ended unresolved, round 2 is under way: its attempts are the
	// current round, and both rounds' fingerprints are excluded.
	l := LedgerFromNotes("i", "h", "b", []string{note("aaaaaaaa"), FinalUnresolved(5), "a person's note", RetryNote(2), note("bbbbbbbb")})
	if l.Round != 2 || l.Closed || len(l.Attempts) != 1 || l.Attempts[0].Fingerprint != "bbbbbbbb" || !l.Fingerprints()["aaaaaaaa"] {
		t.Fatalf("%+v", l)
	}
	// The round ended and nothing restarted it yet: the next claim's For does.
	l = LedgerFromNotes("i", "h", "b", []string{note("aaaaaaaa"), FinalUnresolved(5)})
	if !l.Closed || l.Round != 1 {
		t.Fatalf("%+v", l)
	}
	next, retry := l.For("i", "h", "b", 5)
	if !retry || next.Round != 2 || len(next.Attempts) != 0 || !next.Fingerprints()["aaaaaaaa"] {
		t.Fatalf("%+v %v", next, retry)
	}
}

func TestLedgerForAndRecord(t *testing.T) {
	l, retry := Ledger{}.For("i", "h", "b", 5)
	if retry || l.Round != 1 {
		t.Fatalf("a first ledger: %+v %v", l, retry)
	}
	l.Record(Attempt{N: 1, Fingerprint: "aaaaaaaa", Result: Result{Outcome: MarkersFound}})
	if l.TriedBy("aaaaaaaa") != 1 || !l.Fingerprints()["aaaaaaaa"] {
		t.Fatalf("%+v", l)
	}
	if again, retry := l.For("i", "h", "b", 5); len(again.Attempts) != 1 || retry {
		t.Fatal("the same commits started a new round")
	}
	if moved, retry := l.For("i", "h2", "b", 5); len(moved.Attempts) != 0 || len(moved.Tried) != 0 || retry || moved.Round != 1 {
		t.Fatal("a moved head kept the old attempts")
	}
	for n := 2; n <= 5; n++ {
		l.Record(Attempt{N: n, Fingerprint: strings.Repeat(string(rune('a'+n)), 8), Result: Result{Outcome: MarkersFound}})
	}
	next, retry := l.For("i", "h", "b", 5)
	if !retry || next.Round != 2 || len(next.Attempts) != 0 || len(next.Tried) != 5 {
		t.Fatalf("a spent round: %+v", next)
	}
	// A closed round restarts even when the checkout limit was raised since.
	l.Closed = true
	if raised, retry := l.For("i", "h", "b", 8); !retry || len(raised.Attempts) != 0 {
		t.Fatal("a closed round was continued")
	}
}

func TestLedgerFileRoundTripAndRefusals(t *testing.T) {
	dir := t.TempDir()
	id := "3f9a2c00-0000-4000-8000-000000000000"
	l := Ledger{Item: id, Head: "h", Base: "b"}
	l.Record(Attempt{N: 1, Max: 5, Fingerprint: "aaaaaaaa", Result: Result{Outcome: Passed}})
	if err := WriteLedger(dir, l); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadLedger(dir, id)
	if !ok || got.Head != "h" || len(got.Attempts) != 1 {
		t.Fatalf("%+v %v", got, ok)
	}
	if fi, _ := os.Stat(filepath.Join(dir, id+".json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if err := WriteLedger(dir, Ledger{Item: "../x"}); err == nil {
		t.Fatal("wrote a ledger under a path id")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	if _, ok := ReadLedger(link, id); ok {
		t.Fatal("read through a symlinked directory")
	}
}

func TestParseCard(t *testing.T) {
	body := "```\npr: https://github.com/Owner/Name/pull/12\nrepo: owner/name\nnumber: 12\nhead: feature/x\nbase: main\nfork: false\n```\nResolve the merge conflicts of this pull request.\n"
	c, err := ParseCard(body)
	if err != nil || c.Repo != "owner/name" || c.Number != 12 || c.Head != "feature/x" || c.Base != "main" || c.Fork || c.Host() != "github.com" {
		t.Fatalf("%+v %v", c, err)
	}
	for name, bad := range map[string]string{
		"no block":      "pr: x",
		"other number":  strings.Replace(body, "number: 12", "number: 13", 1),
		"option head":   strings.Replace(body, "head: feature/x", "head: --upload-pack=x", 1),
		"dot-dot base":  strings.Replace(body, "base: main", "base: a/../b", 1),
		"same branches": strings.Replace(body, "head: feature/x", "head: main", 1),
		"bad fork":      strings.Replace(body, "fork: false", "fork: maybe", 1),
		"twice":         strings.Replace(body, "fork: false", "fork: false\nfork: true", 1),
		"missing":       strings.Replace(body, "base: main\n", "", 1),
		"other repo":    strings.Replace(body, "repo: owner/name", "repo: owner/other", 1),
	} {
		if _, err := ParseCard(bad); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestImplicatedNamesOnlyConflictedFiles(t *testing.T) {
	out := "# pkg\n./pkg/a.go:12:3: undefined: x\nb.cs(4,1): error CS0101\nsee c.go for details\n"
	got := Implicated(out, []string{"a.go", "pkg/a.go", "b.cs", "c.go"})
	if !slices.Equal(got, []string{"b.cs", "pkg/a.go"}) {
		t.Fatalf("%v", got)
	}
}
