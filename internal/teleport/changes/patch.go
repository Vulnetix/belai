package changes

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Section is one file's part of a patch, exactly as git wrote it. It is applied
// on its own, so a file that does not apply never holds up the others.
type Section struct {
	Path string
	// Status is added, modified or deleted.
	Status string
	// Blob is the git object id the file has after the change, from the
	// patch's index line; empty for a deletion or a change with no index line.
	// The target hashes its own copy and compares (Check).
	Blob string
	Text string
}

var (
	diffHeader = regexp.MustCompile(`^diff --git a/(\S+) b/(\S+)$`)
	indexLine  = regexp.MustCompile(`^index ([0-9a-f]{40,64})\.\.([0-9a-f]{40,64})( 100(644|755))?$`)
	modeLine   = regexp.MustCompile(`^(old mode|new mode|new file mode|deleted file mode) 100(644|755)$`)
	hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: .*)?$`)
)

// noNewline is the one marker line git writes inside or after a hunk.
const noNewline = `\ No newline at end of file`

// maxPatchLines bounds the lines read, since a patch is untrusted text.
const maxPatchLines = 100_000

// ParsePatch reads a unified patch of the kind `git diff --full-index
// --no-renames` writes and refuses everything else. The grammar is closed: each
// file starts with a `diff --git a/P b/P` line whose two paths are the same safe
// path (SafePath), then only index, mode (100644 or 100755), `---` and `+++`
// lines, then hunks whose line counts match their headers exactly. A rename, a
// copy, a binary patch, a symlink or submodule mode, a path that leaves the
// repository, an extra line anywhere or a count that does not add up is an
// error, so no line of the patch is ever read as something it is not.
func ParsePatch(patch string) ([]Section, error) {
	if patch == "" {
		return nil, errors.New("the patch is empty")
	}
	if !strings.HasSuffix(patch, "\n") {
		return nil, errors.New("the patch does not end with a newline")
	}
	if strings.ContainsRune(patch, 0) {
		return nil, errors.New("the patch holds a NUL byte")
	}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	if len(lines) > maxPatchLines {
		return nil, errors.New("the patch has too many lines")
	}
	var out []Section
	seen := map[string]bool{}
	i := 0
	for i < len(lines) {
		start := i
		m := diffHeader.FindStringSubmatch(lines[i])
		if m == nil || m[1] != m[2] {
			return nil, fmt.Errorf("line %d: expected a diff header for one path", i+1)
		}
		p := m[1]
		if !SafePath(p) {
			return nil, fmt.Errorf("line %d: %q is not a path a patch may change", i+1, clip(p))
		}
		if seen[p] {
			return nil, fmt.Errorf("line %d: %q appears twice", i+1, clip(p))
		}
		seen[p] = true
		i++
		status := "modified"
		blob := ""
		sawIndex := false
		for i < len(lines) && !strings.HasPrefix(lines[i], "--- ") && !strings.HasPrefix(lines[i], "@@ ") && !strings.HasPrefix(lines[i], "diff --git ") {
			l := lines[i]
			switch {
			case indexLine.MatchString(l):
				if sawIndex {
					return nil, fmt.Errorf("line %d: a second index line", i+1)
				}
				sawIndex = true
				blob = indexLine.FindStringSubmatch(l)[2]
			case modeLine.MatchString(l):
				switch {
				case strings.HasPrefix(l, "new file mode"):
					status = "added"
				case strings.HasPrefix(l, "deleted file mode"):
					status = "deleted"
				}
			default:
				return nil, fmt.Errorf("line %d: %q is not a line a patch may carry here", i+1, clip(l))
			}
			i++
		}
		if i < len(lines) && strings.HasPrefix(lines[i], "--- ") {
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "+++ ") {
				return nil, fmt.Errorf("line %d: a --- line without its +++ line", i+1)
			}
			wantOld, wantNew := "--- a/"+p, "+++ b/"+p
			if status == "added" {
				wantOld = "--- /dev/null"
			}
			if status == "deleted" {
				wantNew = "+++ /dev/null"
			}
			if lines[i] != wantOld || lines[i+1] != wantNew {
				return nil, fmt.Errorf("line %d: the file names do not match the header", i+1)
			}
			i += 2
			hunks := 0
			for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
				n, err := readHunk(lines, i)
				if err != nil {
					return nil, err
				}
				i = n
				hunks++
			}
			if hunks == 0 {
				return nil, fmt.Errorf("line %d: a file with names and no hunk", i+1)
			}
		} else if i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			return nil, fmt.Errorf("line %d: a hunk without file names", i+1)
		}
		if i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
			return nil, fmt.Errorf("line %d: %q follows a file's last hunk", i+1, clip(lines[i]))
		}
		if status == "deleted" || strings.Trim(blob, "0") == "" {
			blob = ""
		}
		out = append(out, Section{Path: p, Status: status, Blob: blob, Text: strings.Join(lines[start:i], "\n") + "\n"})
	}
	return out, nil
}

// readHunk reads one hunk starting at lines[i] and returns the index after it.
// Its body must have exactly the old and new line counts its header names.
func readHunk(lines []string, i int) (int, error) {
	m := hunkHeader.FindStringSubmatch(lines[i])
	if m == nil {
		return i, fmt.Errorf("line %d: %q is not a hunk header", i+1, clip(lines[i]))
	}
	old, newN := count(m[2]), count(m[4])
	if old < 0 || newN < 0 {
		return i, fmt.Errorf("line %d: a hunk count is too large", i+1)
	}
	i++
	for old > 0 || newN > 0 {
		if i >= len(lines) {
			return i, errors.New("the patch ends inside a hunk")
		}
		l := lines[i]
		if l == "" {
			return i, fmt.Errorf("line %d: an empty line inside a hunk", i+1)
		}
		if l[0] == '\\' {
			// "\ No newline at end of file" follows the line it applies to, which
			// may be an old line with new lines after it.
			if l != noNewline {
				return i, fmt.Errorf("line %d: an unknown marker", i+1)
			}
			i++
			continue
		}
		switch l[0] {
		case ' ':
			old--
			newN--
		case '-':
			old--
		case '+':
			newN--
		default:
			return i, fmt.Errorf("line %d: %q is not a hunk line", i+1, clip(l))
		}
		if old < 0 || newN < 0 {
			return i, fmt.Errorf("line %d: the hunk is longer than its header says", i+1)
		}
		i++
	}
	// A "\ No newline at end of file" marker may follow either side's last line.
	for i < len(lines) && lines[i] == noNewline {
		i++
	}
	return i, nil
}

// count reads a hunk header's count: absent means 1.
func count(s string) int {
	if s == "" {
		return 1
	}
	n, err := strconv.Atoi(s)
	if err != nil || n > maxPatchLines {
		return -1
	}
	return n
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}
