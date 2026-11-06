package patchfmt

import (
	"strings"
	"testing"
)

func patch(body ...string) string {
	return "*** Begin Patch\n" + strings.Join(body, "\n") + "\n*** End Patch\n"
}

func TestParseSections(t *testing.T) {
	p, err := Parse(patch(
		"*** Update File: a.go",
		"@@ func f()",
		" keep",
		"-old",
		"+new",
		"@@",
		"-two",
		"+deux",
		"*** Add File: dir/b.txt",
		"+one",
		"+",
		"+three",
		"*** Delete File: c.txt",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 3 || p.HunkCount() != 2 {
		t.Fatalf("files=%d hunks=%d", len(p.Files), p.HunkCount())
	}
	u := p.Files[0]
	if u.Op != OpUpdate || u.Path != "a.go" || len(u.Hunks) != 2 {
		t.Fatalf("update = %+v", u)
	}
	if h := u.Hunks[0]; h.Header != "func f()" || h.Old != "keep\nold\n" || h.New != "keep\nnew\n" {
		t.Fatalf("hunk 0 = %+v", h)
	}
	if h := u.Hunks[1]; h.Header != "" || h.Old != "two\n" || h.New != "deux\n" {
		t.Fatalf("hunk 1 = %+v", h)
	}
	if a := p.Files[1]; a.Op != OpAdd || a.Content != "one\n\nthree\n" {
		t.Fatalf("add = %+v", a)
	}
	if d := p.Files[2]; d.Op != OpDelete || d.Path != "c.txt" {
		t.Fatalf("delete = %+v", d)
	}
}

func TestParseFirstHunkNeedsNoMarker(t *testing.T) {
	p, err := Parse(patch("*** Update File: a", "-x", "+y"))
	if err != nil || len(p.Files[0].Hunks) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestParseEmptyLineIsBlankContext(t *testing.T) {
	p, err := Parse(patch("*** Update File: a", " one", "", " two", "-x", "+y"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Files[0].Hunks[0].Old; got != "one\n\ntwo\nx\n" {
		t.Fatalf("old = %q", got)
	}
}

func TestParseDropsBoundaryBlankLines(t *testing.T) {
	p, err := Parse(patch("*** Update File: a", "-x", "+y", "", "@@", "-p", "+q", "", "*** Add File: b", "+z", ""))
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Hunks[0].Old != "x\n" || p.Files[0].Hunks[1].Old != "p\n" || p.Files[1].Content != "z\n" {
		t.Fatalf("%+v", p.Files)
	}
}

func TestParseKeepsBlankBeforeEndOfFile(t *testing.T) {
	p, err := Parse(patch("*** Update File: a", " ctx", "-x", "+y", "", "*** End of File"))
	if err != nil {
		t.Fatal(err)
	}
	h := p.Files[0].Hunks[0]
	if !h.AtEnd || h.Old != "ctx\nx\n\n" {
		t.Fatalf("%+v", h)
	}
}

func TestParseUnifiedHeaderNamesNoScope(t *testing.T) {
	p, err := Parse(patch("*** Update File: a", "@@ -1,3 +1,4 @@ func f", "-x", "+y"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Hunks[0].Header != "" {
		t.Fatalf("header = %q", p.Files[0].Hunks[0].Header)
	}
}

func TestParseToleratesCRLFMarkers(t *testing.T) {
	text := "*** Begin Patch\r\n*** Update File: a\r\n-x\r\n+y\r\n*** End Patch\r\n"
	// A body line keeps its carriage return: it is part of the text.
	p, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Path != "a" || p.Files[0].Hunks[0].Old != "x\r\n" {
		t.Fatalf("%+v", p.Files[0])
	}
}

func TestParseRefusals(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", "Begin Patch"},
		{"no begin", "*** Update File: a\n-x\n+y\n*** End Patch\n", "Begin Patch"},
		{"no end", "*** Begin Patch\n*** Update File: a\n-x\n+y\n", "End Patch"},
		{"nothing", "*** Begin Patch\n*** End Patch\n", "names no file"},
		{"text after end", patch("*** Delete File: a") + "extra\n", "after"},
		{"unknown section", patch("*** Move File: a"), "expected a file section"},
		{"stray text", patch("hello"), "expected a file section"},
		{"no path", patch("*** Delete File: "), "no path"},
		{"padded path", patch("*** Delete File:  a"), "space"},
		{"control path", patch("*** Delete File: a\x1bb"), "refused"},
		{"duplicate", patch("*** Delete File: a", "*** Update File: a", "-x", "+y"), "two sections"},
		{"update without hunk", patch("*** Update File: a", "*** Delete File: b"), "no hunk"},
		{"hunk without change", patch("*** Update File: a", " x"), "no `-` or `+`"},
		{"same text", patch("*** Update File: a", "-x", "+x"), "same text"},
		{"add only needs context", patch("*** Update File: a", "+x"), "only added lines"},
		{"bad hunk line", patch("*** Update File: a", "x"), "must start with"},
		{"eof without hunk", patch("*** Update File: a", "*** End of File"), "no hunk"},
		{"add without plus", patch("*** Add File: a", "text"), "must start with `+`"},
		{"add blank in middle", patch("*** Add File: a", "+x", "", "+y"), "must start with `+`"},
		{"nul", "*** Begin Patch\n*** Delete File: a\x00\n*** End Patch\n", "NUL"},
		{"invalid utf8", "*** Begin Patch\n*** Delete File: a\xff\n*** End Patch\n", "UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.in)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q lacks %q", err, c.want)
			}
		})
	}
}

func TestParseErrorsNeverQuoteBodyText(t *testing.T) {
	secret := "SECRET-TOKEN-1234"
	for _, in := range []string{
		patch("*** Update File: a", secret),
		patch("*** Add File: a", secret),
		patch(secret),
		patch("*** Update File: a", "-"+secret, "+"+secret),
		patch("*** Update File: a", "+"+secret),
	} {
		if _, err := Parse(in); err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("error quotes patch text: %v", err)
		}
	}
}

func TestParseCaps(t *testing.T) {
	var files []string
	for i := 0; i <= MaxFiles; i++ {
		files = append(files, "*** Delete File: f"+strings.Repeat("x", 1)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	if _, err := Parse(patch(files...)); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("file cap: %v", err)
	}
	var hunks []string
	hunks = append(hunks, "*** Update File: a")
	for i := 0; i <= MaxHunks; i++ {
		hunks = append(hunks, "@@", "-x", "+y")
	}
	if _, err := Parse(patch(hunks...)); err == nil || !strings.Contains(err.Error(), "hunks") {
		t.Fatalf("hunk cap: %v", err)
	}
	big := patch("*** Add File: a", "+"+strings.Repeat("z", MaxBytes))
	if _, err := Parse(big); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size cap: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", "*** Begin Patch\n*** End Patch\n",
		patch("*** Update File: a", "@@ x", " c", "-o", "+n", "*** End of File"),
		patch("*** Add File: a", "+x"),
		patch("*** Delete File: a"),
		"*** Begin Patch\r\n*** Update File: a\r\n-x\r\n+y\r\n*** End Patch",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		p, err := Parse(in)
		if err != nil {
			return
		}
		if len(p.Files) == 0 || len(p.Files) > MaxFiles || p.HunkCount() > MaxHunks {
			t.Fatalf("parsed outside the caps: %d files, %d hunks", len(p.Files), p.HunkCount())
		}
		seen := map[string]bool{}
		for _, f := range p.Files {
			if f.Path == "" || seen[f.Path] {
				t.Fatalf("bad or repeated path %q", f.Path)
			}
			seen[f.Path] = true
			if f.Op == OpUpdate && len(f.Hunks) == 0 {
				t.Fatal("an update without hunks")
			}
			for _, h := range f.Hunks {
				if h.Old == h.New {
					t.Fatal("a hunk that changes nothing")
				}
			}
		}
	})
}
