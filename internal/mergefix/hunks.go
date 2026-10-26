// Package mergefix holds the rules of the merge-conflict loop
// (docs/merge-conflicts.md): it parses the conflict hunks git leaves in a
// merged tree, classifies how each hunk was resolved, fingerprints a
// resolution, keeps the ledger of attempts, finds duplicate imports and
// leftover conflict markers, and plans the strategy for the next attempt.
//
// Everything here is a pure function of file contents and harness facts. No
// model is asked anything, and nothing a model wrote is ever returned as text:
// a classification is an enum, a fingerprint is a hash, a file name is a
// cleaned path from git's own index, and a note is composed from those.
package mergefix

import (
	"errors"
	"fmt"
	"strings"
)

// The conflict markers git writes with the default marker size.
const (
	markerOurs   = "<<<<<<<"
	markerBase   = "|||||||"
	markerSplit  = "======="
	markerTheirs = ">>>>>>>"
)

// MaxHunks bounds the hunks read from one file.
const MaxHunks = 400

// Hunk is one conflict region. Ours is the current side (the pull request's
// head, checked out before the merge), Theirs the incoming side (the base
// merged into it), and Base their common ancestor when git wrote a diff3
// section.
type Hunk struct {
	// Index is the hunk's position in its file, from 0.
	Index int
	// Line is the 1-based line of the <<<<<<< marker in the conflicted file.
	Line               int
	Ours, Base, Theirs []string
}

// File is one conflicted file as the merge left it.
type File struct {
	Path string
	// Hunks are the conflict regions, and Common the text around them:
	// Common[i] precedes Hunks[i] and Common[len(Hunks)] ends the file.
	Hunks  []Hunk
	Common [][]string
	// Whole marks a conflict git could not write markers for: a binary file, a
	// file one side deleted, a symlink. It is one choice for the whole file,
	// compared by blob id: OursBlob and TheirsBlob, "" for a side that has no
	// such file.
	Whole      bool
	OursBlob   string
	TheirsBlob string
}

// HunkCount is the number of choices the file holds: one for a whole-file
// conflict.
func (f File) HunkCount() int {
	if f.Whole {
		return 1
	}
	return len(f.Hunks)
}

// SplitLines splits text into lines without their line endings.
func SplitLines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// isMarker reports whether line is the marker m, alone or followed by a space
// and a label, as git writes it.
func isMarker(line, m string) bool {
	if !strings.HasPrefix(line, m) {
		return false
	}
	rest := line[len(m):]
	return rest == "" || rest[0] == ' ' || rest[0] == '\t'
}

func isSplit(line string) bool { return strings.TrimRight(line, " \t") == markerSplit }

// ErrMalformed means a file's conflict markers do not nest as git writes them.
var ErrMalformed = errors.New("mergefix: malformed conflict markers")

// ParseFile reads the conflict hunks of one file the merge left with markers.
// A stray closing or base marker outside a hunk is kept as text; an opening
// marker inside a hunk, or a hunk that never closes, is ErrMalformed.
func ParseFile(path string, content []byte) (File, error) {
	f := File{Path: path}
	var common []string
	var h Hunk
	state := 0 // 0 outside, 1 ours, 2 base, 3 theirs
	for i, l := range SplitLines(content) {
		switch state {
		case 0:
			if isMarker(l, markerOurs) {
				if len(f.Hunks) >= MaxHunks {
					return File{}, fmt.Errorf("%w: more than %d hunks", ErrMalformed, MaxHunks)
				}
				f.Common = append(f.Common, common)
				common = nil
				h = Hunk{Index: len(f.Hunks), Line: i + 1}
				state = 1
				continue
			}
			common = append(common, l)
		case 1:
			switch {
			case isMarker(l, markerBase):
				state = 2
			case isSplit(l):
				state = 3
			case isMarker(l, markerOurs):
				return File{}, fmt.Errorf("%w: a hunk opens inside the hunk at line %d", ErrMalformed, h.Line)
			default:
				h.Ours = append(h.Ours, l)
			}
		case 2:
			switch {
			case isSplit(l):
				state = 3
			case isMarker(l, markerOurs):
				return File{}, fmt.Errorf("%w: a hunk opens inside the hunk at line %d", ErrMalformed, h.Line)
			default:
				h.Base = append(h.Base, l)
			}
		case 3:
			switch {
			case isMarker(l, markerTheirs):
				f.Hunks = append(f.Hunks, h)
				state = 0
			case isMarker(l, markerOurs):
				return File{}, fmt.Errorf("%w: a hunk opens inside the hunk at line %d", ErrMalformed, h.Line)
			default:
				h.Theirs = append(h.Theirs, l)
			}
		}
	}
	if state != 0 {
		return File{}, fmt.Errorf("%w: the hunk at line %d never closes", ErrMalformed, h.Line)
	}
	f.Common = append(f.Common, common)
	return f, nil
}

// MarkerCount counts the lines of content that are an opening, base or closing
// conflict marker. A separator line alone (=======) is ordinary text in many
// formats, so it is not counted.
func MarkerCount(content []byte) int {
	n := 0
	for _, l := range SplitLines(content) {
		if isMarker(l, markerOurs) || isMarker(l, markerTheirs) || isMarker(l, markerBase) {
			n++
		}
	}
	return n
}

// MarkersLeft reports whether a resolved file still holds conflict markers the
// two sides did not already have: a file that documents the markers (a merge
// tool's own tests) keeps its own, and only new ones count.
func MarkersLeft(resolved, ours, theirs []byte) bool {
	return MarkerCount(resolved) > max(MarkerCount(ours), MarkerCount(theirs))
}
