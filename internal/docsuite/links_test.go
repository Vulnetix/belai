package docsuite

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/vulnetix/belai/internal/docparity"
)

var (
	mdLink  = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	heading = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*#*\s*$`)
)

// slug is how GitHub turns a heading into an anchor: lower case, punctuation
// other than hyphens and underscores removed, spaces turned into hyphens.
func slug(h string) string {
	h = strings.ReplaceAll(h, "`", "")
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// anchors returns every heading anchor in a Markdown file, with GitHub's -1,
// -2 suffix for a repeated heading.
func anchors(src string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			s := slug(m[1])
			if n := seen[s]; n > 0 {
				out[s+"-"+strconv.Itoa(n)] = true
			} else {
				out[s] = true
			}
			seen[s]++
		}
	}
	return out
}

// TestEveryRelativeDocLinkResolves checks each relative link in the docs, the
// README and AGENTS.md: the file exists and, when the link names a section, the
// target file has a heading with that anchor.
func TestEveryRelativeDocLinkResolves(t *testing.T) {
	root := docparity.Root(t)
	var files []string
	_ = filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			files = append(files, p)
		}
		return nil
	})
	files = append(files, filepath.Join(root, "README.md"), filepath.Join(root, "AGENTS.md"))
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range mdLink.FindAllStringSubmatch(src, -1) {
			link := m[1]
			if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
				continue
			}
			path, frag, _ := strings.Cut(link, "#")
			target := f
			if path != "" {
				target = filepath.Join(filepath.Dir(f), path)
			}
			info, err := os.Stat(target)
			if err != nil {
				t.Errorf("%s links to %s, which does not exist", rel(root, f), link)
				continue
			}
			checked++
			if frag == "" || info.IsDir() || !strings.HasSuffix(target, ".md") {
				continue
			}
			tb, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !anchors(string(tb))[frag] {
				t.Errorf("%s links to %s, but %s has no heading #%s", rel(root, f), link, rel(root, target), frag)
			}
		}
	}
	if checked < 200 {
		t.Fatalf("only %d links checked; the pattern is wrong", checked)
	}
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

// TestEveryDocsPageIsInTheIndex keeps docs/README.md complete: each page under
// docs/ is linked from it, so a new page cannot go unlisted.
func TestEveryDocsPageIsInTheIndex(t *testing.T) {
	root := docparity.Root(t)
	index := docparity.Read(t, "docs/README.md")
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		pages++
		if !strings.Contains(index, "]("+e.Name()+")") && !strings.Contains(index, "]("+e.Name()+"#") {
			t.Errorf("docs/README.md does not link %s", e.Name())
		}
	}
	if pages < 40 {
		t.Fatalf("only %d pages found; the walk is wrong", pages)
	}
}
