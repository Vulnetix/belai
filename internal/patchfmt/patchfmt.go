// Package patchfmt reads the patch envelope of the apply_patch tool and
// anchors its hunks in a file.
//
// The envelope is a closed grammar. It starts with `*** Begin Patch`, ends with
// `*** End Patch`, and holds file sections:
//
//	*** Update File: path    hunks of context, "-" and "+" lines
//	*** Add File: path       "+" lines, the whole new file
//	*** Delete File: path    nothing else
//
// An Update hunk may begin with `@@` or `@@ line`, where the text names one line
// of the file that the hunk follows, and may end with `*** End of File` to say
// it sits at the end of the file. Any other line is an error: no line of a patch
// is ever read as something it is not. The parser is bounded (MaxFiles,
// MaxHunks, MaxBytes) because the patch is a model's text.
//
// Anchoring (Plan) finds each hunk's old lines in the file through a fixed
// ladder, the first rung that finds exactly one place deciding: whole lines
// exact, line endings folded, trailing spaces and tabs ignored, one consistent
// indentation shift, and, only when the caller allows it, Unicode punctuation
// folded to ASCII. Zero or several places fail the whole patch. Nothing here
// reads or writes a file, and no error quotes file or hunk text: an error
// carries a path, a hunk number and a measured kind of difference, because file
// bytes are arbitrary and only Read classifies them. See docs/patch-tool.md.
package patchfmt

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Bounds on a patch.
const (
	MaxFiles = 64
	MaxHunks = 256
	MaxBytes = 1 << 20
)

// Op is what a file section does.
type Op int

const (
	// OpUpdate changes an existing file with hunks.
	OpUpdate Op = iota + 1
	// OpAdd creates a file that does not exist.
	OpAdd
	// OpDelete removes an existing file.
	OpDelete
)

// String names the op as the result and the docs do.
func (o Op) String() string {
	switch o {
	case OpUpdate:
		return "update"
	case OpAdd:
		return "add"
	case OpDelete:
		return "delete"
	}
	return "none"
}

// Hunk is one change inside an Update section.
type Hunk struct {
	// Header is the text after `@@`, trimmed. When set it must match exactly
	// one line of the file, and the hunk is searched for after that line.
	Header string
	// Old holds the context and removed lines, each ended by a newline. New
	// holds the context and added lines the same way.
	Old, New string
	// AtEnd is set by `*** End of File`: the hunk sits at the end of the file.
	AtEnd bool
}

// File is one section of a patch.
type File struct {
	Op   Op
	Path string
	// Hunks are the changes of an Update.
	Hunks []Hunk
	// Content is the text of an Add, every line ended by a newline.
	Content string
}

// Patch is a parsed envelope. Files keep the order they were written in.
type Patch struct {
	Files []File
}

// HunkCount is the number of hunks in the patch.
func (p *Patch) HunkCount() int {
	n := 0
	for _, f := range p.Files {
		n += len(f.Hunks)
	}
	return n
}

const (
	beginMarker  = "*** Begin Patch"
	endMarker    = "*** End Patch"
	updatePrefix = "*** Update File:"
	addPrefix    = "*** Add File:"
	deletePrefix = "*** Delete File:"
	eofMarker    = "*** End of File"
)

// unifiedHeader is the `-1,3 +1,4 @@` form some writers put after @@. The
// numbers are never trusted, so such a header names no scope line.
var unifiedHeader = regexp.MustCompile(`^-\d+(,\d+)? \+\d+(,\d+)? @@.*$`)

// Parse reads a patch. Every error names a line number and a rule, never the
// text of a line.
func Parse(text string) (*Patch, error) {
	if len(text) > MaxBytes {
		return nil, fmt.Errorf("the patch is over %d bytes", MaxBytes)
	}
	if strings.ContainsRune(text, 0) {
		return nil, errors.New("the patch holds a NUL byte")
	}
	if !utf8.ValidString(text) {
		return nil, errors.New("the patch is not valid UTF-8")
	}
	lines := strings.Split(text, "\n")
	// A trailing newline leaves one empty element.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || marker(lines[i]) != beginMarker {
		return nil, errors.New("the patch must start with `*** Begin Patch`")
	}
	i++
	p := &Patch{}
	seen := map[string]bool{}
	for i < len(lines) {
		m := marker(lines[i])
		if m == endMarker {
			for i++; i < len(lines); i++ {
				if strings.TrimSpace(lines[i]) != "" {
					return nil, fmt.Errorf("line %d: text after `*** End Patch`", i+1)
				}
			}
			if len(p.Files) == 0 {
				return nil, errors.New("the patch names no file")
			}
			return p, nil
		}
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		var f File
		switch {
		case strings.HasPrefix(m, updatePrefix):
			f.Op, f.Path = OpUpdate, strings.TrimPrefix(strings.TrimPrefix(m, updatePrefix), " ")
		case strings.HasPrefix(m, addPrefix):
			f.Op, f.Path = OpAdd, strings.TrimPrefix(strings.TrimPrefix(m, addPrefix), " ")
		case strings.HasPrefix(m, deletePrefix):
			f.Op, f.Path = OpDelete, strings.TrimPrefix(strings.TrimPrefix(m, deletePrefix), " ")
		default:
			return nil, fmt.Errorf("line %d: expected a file section (`*** Update File:`, `*** Add File:`, `*** Delete File:`) or `*** End Patch`", i+1)
		}
		if err := checkPath(f.Path); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if len(p.Files) >= MaxFiles {
			return nil, fmt.Errorf("line %d: the patch names more than %d files", i+1, MaxFiles)
		}
		if seen[f.Path] {
			return nil, fmt.Errorf("line %d: %s appears in two sections; put every change to a file in one", i+1, sanitize.Line(f.Path, 120))
		}
		seen[f.Path] = true
		i++
		var err error
		switch f.Op {
		case OpUpdate:
			i, err = parseHunks(lines, i, &f, p.HunkCount())
		case OpAdd:
			i, err = parseAdd(lines, i, &f)
		}
		if err != nil {
			return nil, err
		}
		p.Files = append(p.Files, f)
	}
	return nil, errors.New("the patch must end with `*** End Patch`")
}

// marker returns a line with a trailing carriage return and spaces removed when
// it is one of the envelope's `***` lines, and the line unchanged otherwise.
func marker(line string) string {
	if !strings.HasPrefix(line, "***") {
		return line
	}
	return strings.TrimRight(line, " \t\r")
}

// checkPath refuses a path no tool should resolve: empty, with a control,
// bidirectional or invisible rune. Confinement is checked by the caller against
// the session roots; this only keeps the text sane.
func checkPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("a file section has no path")
	}
	if p != strings.TrimSpace(p) {
		return errors.New("a path has leading or trailing space")
	}
	if err := sanitize.PathText(p); err != nil {
		return fmt.Errorf("the path %s is refused: %w", sanitize.Line(p, 80), err)
	}
	return nil
}

func isSectionStart(m string) bool {
	return m == endMarker ||
		strings.HasPrefix(m, updatePrefix) || strings.HasPrefix(m, addPrefix) || strings.HasPrefix(m, deletePrefix)
}

// parseHunks reads the hunks of an Update section from lines[i:], returning the
// index of the first line after them. have is how many hunks the patch holds so
// far.
func parseHunks(lines []string, i int, f *File, have int) (int, error) {
	var cur *Hunk
	var old, nw strings.Builder
	changed := false
	startLine := 0
	flush := func() error {
		if cur == nil {
			return nil
		}
		if !changed {
			return fmt.Errorf("line %d: a hunk has no `-` or `+` line", startLine)
		}
		cur.Old, cur.New = old.String(), nw.String()
		if cur.Old == cur.New {
			return fmt.Errorf("line %d: a hunk replaces text with the same text", startLine)
		}
		if cur.Old == "" && !cur.AtEnd {
			return fmt.Errorf("line %d: a hunk with only added lines needs a context or removed line, or `*** End of File`", startLine)
		}
		f.Hunks = append(f.Hunks, *cur)
		cur = nil
		old.Reset()
		nw.Reset()
		changed = false
		return nil
	}
	begin := func(n int, header string) error {
		if have+len(f.Hunks) >= MaxHunks {
			return fmt.Errorf("line %d: the patch holds more than %d hunks", n, MaxHunks)
		}
		cur = &Hunk{Header: header}
		startLine = n
		return nil
	}
	// blanks counts empty lines not yet placed. An empty line inside a hunk is
	// a blank context line, but a run of them at a hunk's boundary is
	// separator spacing and is dropped: that only shortens the context.
	blanks := 0
	place := func() {
		for ; blanks > 0; blanks-- {
			old.WriteString("\n")
			nw.WriteString("\n")
		}
	}
	for ; i < len(lines); i++ {
		line := lines[i]
		m := marker(line)
		if isSectionStart(m) {
			break
		}
		if m == eofMarker {
			if cur == nil {
				return i, fmt.Errorf("line %d: `*** End of File` with no hunk", i+1)
			}
			place()
			cur.AtEnd = true
			if err := flush(); err != nil {
				return i, err
			}
			continue
		}
		if line == "@@" || strings.HasPrefix(line, "@@ ") {
			blanks = 0
			if err := flush(); err != nil {
				return i, err
			}
			header := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
			if unifiedHeader.MatchString(header) {
				header = ""
			}
			if err := begin(i+1, header); err != nil {
				return i, err
			}
			continue
		}
		if line == "" {
			if cur != nil {
				blanks++
			}
			continue
		}
		if cur == nil {
			if err := begin(i+1, ""); err != nil {
				return i, err
			}
		}
		switch line[0] {
		case ' ':
			place()
			old.WriteString(line[1:] + "\n")
			nw.WriteString(line[1:] + "\n")
		case '-':
			place()
			old.WriteString(line[1:] + "\n")
			changed = true
		case '+':
			place()
			nw.WriteString(line[1:] + "\n")
			changed = true
		default:
			return i, fmt.Errorf("line %d: a hunk line must start with a space, `-` or `+`", i+1)
		}
	}
	blanks = 0
	if err := flush(); err != nil {
		return i, err
	}
	if len(f.Hunks) == 0 {
		return i, fmt.Errorf("the section for %s has no hunk", sanitize.Line(f.Path, 120))
	}
	return i, nil
}

// parseAdd reads the `+` lines of an Add section.
func parseAdd(lines []string, i int, f *File) (int, error) {
	var b strings.Builder
	blank := 0 // 1-based line number of the first blank line not yet followed by text
	for ; i < len(lines); i++ {
		line := lines[i]
		if isSectionStart(marker(line)) {
			break
		}
		if line == "" {
			if blank == 0 {
				blank = i + 1
			}
			continue
		}
		if blank != 0 || line[0] != '+' {
			n := i + 1
			if blank != 0 {
				n = blank
			}
			return i, fmt.Errorf("line %d: a line of an added file must start with `+`", n)
		}
		b.WriteString(line[1:] + "\n")
	}
	f.Content = b.String()
	return i, nil
}
