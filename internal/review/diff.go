package review

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/workdiff"
)

// Limits bound what one review covers.
type Limits struct {
	// MaxBytes caps the diff text a review covers (review.max_diff_kb).
	MaxBytes int
	// MaxFiles caps the reviewable files; zero is MaxDiffFiles.
	MaxFiles int
}

func (l Limits) files() int {
	if l.MaxFiles <= 0 || l.MaxFiles > MaxDiffFiles {
		return MaxDiffFiles
	}
	return l.MaxFiles
}

// Why a changed file is not reviewable.
const (
	skipBinary  = "binary"
	skipDeleted = "deleted"
	skipPath    = "path with unusual characters"
	skipHidden  = "a Read deny rule covers it"
	skipBytes   = "past the diff size cap"
	skipFiles   = "past the file cap"
)

// DiffFile is one changed file as the harness read it.
type DiffFile struct {
	// Path is repository-relative with slashes and made only of letters, digits
	// and . _ - / @ + for a reviewable file.
	Path           string
	Added, Removed int
	// Skip says why the file is not reviewable, or is empty when it is.
	Skip string

	spans []span
	size  int
}

type span struct{ from, to int }

// Reviewable reports whether findings may name the file.
func (f DiffFile) Reviewable() bool { return f.Skip == "" }

// HasLine reports whether line n of the new file lies inside a hunk of the
// diff (a changed line or the context around one).
func (f DiffFile) HasLine(n int) bool {
	if !f.Reviewable() {
		return false
	}
	for _, s := range f.spans {
		if n >= s.from && n <= s.to {
			return true
		}
	}
	return false
}

// Diff is the change under review: which files, and where in them.
type Diff struct {
	Files []DiffFile
	// Truncated reports that a cap left changed files out of the review.
	Truncated bool
}

// Reviewable counts the files findings may name.
func (d Diff) Reviewable() int {
	n := 0
	for _, f := range d.Files {
		if f.Reviewable() {
			n++
		}
	}
	return n
}

// Skipped counts the changed files that are not reviewable.
func (d Diff) Skipped() int { return len(d.Files) - d.Reviewable() }

// File finds a file by path.
func (d Diff) File(path string) (DiffFile, bool) {
	for _, f := range d.Files {
		if f.Path == path {
			return f, true
		}
	}
	return DiffFile{}, false
}

// HasLine reports whether path is a reviewable file of the diff and line lies
// inside one of its hunks.
func (d Diff) HasLine(path string, line int) bool {
	f, ok := d.File(path)
	return ok && f.HasLine(line)
}

// PlainPath reports whether p is a repository-relative path made only of
// letters, digits and . _ - / @ +, the shape a path must have to be named in
// a prompt or a finding. It refuses an absolute path, a ".." segment and
// anything under .git.
func PlainPath(p string) bool {
	if p == "" || len(p) > 400 || strings.HasPrefix(p, "/") || sanitize.PathText(p) != nil {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == '/', r == '@', r == '+':
		default:
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == ".." || seg == "." || seg == ".git" {
			return false
		}
	}
	return true
}

// admit applies the caps in file order: a file that would take the diff past
// the byte cap, or past the file cap, is skipped and the diff marked truncated.
func admit(files []DiffFile, lim Limits) Diff {
	d := Diff{Files: files}
	used, n := 0, 0
	for i := range d.Files {
		f := &d.Files[i]
		if !f.Reviewable() {
			continue
		}
		switch {
		case n >= lim.files():
			f.Skip, f.spans = skipFiles, nil
			d.Truncated = true
		case lim.MaxBytes > 0 && used+f.size > lim.MaxBytes:
			f.Skip, f.spans = skipBytes, nil
			d.Truncated = true
		default:
			used += f.size
			n++
		}
	}
	return d
}

// ParseUnified reads a unified diff (git diff, gh pr diff, glab mr diff) into
// a Diff. hide, when non-nil, is asked for each path and returns true for a
// file a Read deny rule covers: it is listed by name and never reviewed. The
// text is third-party text: only paths that pass PlainPath, hunk positions and
// counts are taken from it, and a header that does not parse skips the file.
func ParseUnified(text string, lim Limits, hide func(path string) bool) Diff {
	var files []DiffFile
	var cur *DiffFile
	oldRem, newRem := 0, 0
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Skip == "" && hide != nil && hide(cur.Path) {
			cur.Skip, cur.spans = skipHidden, nil
		}
		files = append(files, *cur)
		cur = nil
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if oldRem > 0 || newRem > 0 {
			var c byte
			if line != "" {
				c = line[0]
			}
			consumed := true
			switch {
			case c == '+' && newRem > 0:
				cur.Added++
				newRem--
			case c == '-' && oldRem > 0:
				cur.Removed++
				oldRem--
			case (c == ' ' || line == "") && oldRem > 0 && newRem > 0:
				oldRem--
				newRem--
			case c == '\\':
			default:
				consumed = false
			}
			if consumed {
				cur.size += len(line) + 1
				continue
			}
			// The hunk ended early: its counts were wrong. Read the line as a
			// header rather than lose the next file.
			oldRem, newRem = 0, 0
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			p, ok := headerPath(strings.TrimPrefix(line, "diff --git "))
			cur = &DiffFile{Path: p}
			if !ok || !PlainPath(p) {
				cur.Skip = skipPath
			}
			cur.size = len(line) + 1
		case cur == nil:
		case strings.HasPrefix(line, "+++ "):
			cur.size += len(line) + 1
			target := strings.TrimSuffix(strings.TrimPrefix(line, "+++ "), "\t")
			switch {
			case target == "/dev/null":
				if cur.Skip == "" {
					cur.Skip = skipDeleted
				}
			case strings.HasPrefix(target, "b/"):
				cur.Path = strings.TrimPrefix(target, "b/")
				if cur.Skip == skipPath || cur.Skip == "" {
					cur.Skip = ""
					if !PlainPath(cur.Path) {
						cur.Skip = skipPath
					}
				}
			}
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "new file mode"),
			strings.HasPrefix(line, "old mode"), strings.HasPrefix(line, "new mode"), strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "rename from"):
			cur.size += len(line) + 1
		case strings.HasPrefix(line, "rename to "):
			cur.size += len(line) + 1
			if p := strings.TrimPrefix(line, "rename to "); PlainPath(p) {
				cur.Path = p
				if cur.Skip == skipPath {
					cur.Skip = ""
				}
			} else {
				cur.Skip = skipPath
			}
		case strings.HasPrefix(line, "deleted file mode"):
			cur.size += len(line) + 1
			if cur.Skip == "" {
				cur.Skip = skipDeleted
			}
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			cur.size += len(line) + 1
			if cur.Skip == "" {
				cur.Skip = skipBinary
			}
		case strings.HasPrefix(line, "@@ "):
			cur.size += len(line) + 1
			o, n, start, ok := hunkHeader(line)
			if !ok {
				cur.Skip, cur.spans = skipPath, nil
				continue
			}
			oldRem, newRem = o, n
			if n > 0 {
				cur.spans = append(cur.spans, span{from: start, to: start + n - 1})
			}
		}
	}
	flush()
	return admit(files, lim)
}

// headerPath returns the new path of a "diff --git a/P b/Q" header. With no
// rename P and Q are equal, which fixes where a path holding " b/" splits.
func headerPath(rest string) (string, bool) {
	if strings.HasPrefix(rest, `"`) || !strings.HasPrefix(rest, "a/") {
		return "", false
	}
	best, found := "", false
	for i := 2; i+3 <= len(rest); i++ {
		if !strings.HasPrefix(rest[i:], " b/") {
			continue
		}
		p, q := rest[2:i], rest[i+3:]
		if p == q {
			return q, true
		}
		if !found {
			best, found = q, true
		}
	}
	return best, found
}

// hunkHeader reads "@@ -a,b +c,d @@" and returns b, d and c.
func hunkHeader(line string) (oldN, newN, newStart int, ok bool) {
	f := strings.Fields(line)
	if len(f) < 4 || f[0] != "@@" || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, 0, false
	}
	_, oldN, ok1 := rangeOf(f[1][1:])
	newStart, newN, ok2 := rangeOf(f[2][1:])
	if !ok1 || !ok2 {
		return 0, 0, 0, false
	}
	return oldN, newN, newStart, true
}

// rangeOf reads "start,count" or "start" (a count of one).
func rangeOf(s string) (start, count int, ok bool) {
	a, b, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		if count, err = strconv.Atoi(b); err != nil || count < 0 {
			return 0, 0, false
		}
	}
	// A hunk cannot claim more lines than a diff this size could hold.
	if count > 1<<22 {
		return 0, 0, false
	}
	return start, count, true
}

// FromSnapshot builds a Diff from the working tree's changes, as the /diff
// pane collected them. A hidden file keeps its name and nothing else.
func FromSnapshot(s workdiff.Snapshot, lim Limits) Diff {
	files := make([]DiffFile, 0, len(s.Files))
	for _, f := range s.Files {
		df := DiffFile{Path: f.Path, Added: f.Added, Removed: f.Removed}
		switch {
		case f.Hidden:
			df.Skip = skipHidden
		case !PlainPath(f.Path):
			df.Skip = skipPath
		case f.Change.Binary:
			df.Skip = skipBinary
		case f.Change.Deleted:
			df.Skip = skipDeleted
		case f.Note != "" && len(f.Change.Rows()) == 0:
			df.Skip = f.Note
		default:
			for _, r := range f.Change.Rows() {
				df.size += len(r.Text) + 2
				if r.NewLine > 0 && (r.Op == filediff.OpAdd || r.Op == filediff.OpContext) {
					df.spans = extend(df.spans, r.NewLine)
				}
			}
		}
		files = append(files, df)
	}
	d := admit(files, lim)
	if s.More > 0 {
		d.Truncated = true
	}
	return d
}

// extend adds line n to the last span when it is the next line, else starts a
// new span.
func extend(spans []span, n int) []span {
	if k := len(spans); k > 0 && n == spans[k-1].to+1 {
		spans[k-1].to = n
		return spans
	}
	return append(spans, span{from: n, to: n})
}
