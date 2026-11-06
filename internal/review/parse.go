package review

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Parsed is what a reviewer's reply held once the harness had read it against
// the diff.
type Parsed struct {
	// Verdict is the word after the first valid "verdict:" line; HaveVerdict is
	// false when there was none.
	Verdict     Verdict
	HaveVerdict bool
	// Findings are the validated findings, most serious first.
	Findings []Finding
	// Dropped counts finding lines that failed a check or came past the limit.
	Dropped int
}

// findingLine is the grammar of one finding:
//
//	finding: FILE:LINE: SEVERITY RUBRIC: what is wrong and what would fix it
//
// SEVERITY and RUBRIC are words from the closed lists, separated by a space or
// a slash. An optional following "fix: text" line attaches a suggested fix.
var findingLine = regexp.MustCompile(`(?i)^finding:\s*(\S+?):(\d{1,9}):\s*([a-z]+)[\s/]+([a-z]+)\s*:\s*(\S.*)$`)

// Parse reads the reply of a review turn. Only lines that fit the grammar are
// read; everything else (reasoning, a summary, a stray heading) is ignored. A
// finding is kept only when its file is a reviewable file of d, its line lies
// inside one of that file's hunks, its severity and rubric are in the closed
// lists and its body is non-empty and at most MaxBodyBytes once cleaned. The
// rest are counted in Dropped and never repaired. At most MaxFindings finding
// lines are read.
func Parse(reply string, d Diff) Parsed {
	if len(reply) > MaxReplyBytes {
		reply = reply[:MaxReplyBytes]
	}
	var out Parsed
	seen := map[string]bool{}
	read := 0
	last := -1 // index of the finding a "fix:" line would attach to
	for _, raw := range strings.Split(sanitize.Text(reply), "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimLeft(line, "-*• \t")
		if strings.HasPrefix(line, "`") {
			line = strings.Trim(line, "`")
		}
		low := strings.ToLower(line)
		switch {
		case strings.HasPrefix(low, "verdict:"):
			if out.HaveVerdict {
				continue
			}
			word := strings.ToLower(strings.TrimSpace(line[len("verdict:"):]))
			if i := strings.IndexAny(word, " \t.,;"); i >= 0 {
				word = word[:i]
			}
			for _, v := range Verdicts {
				if word == string(v) {
					out.Verdict, out.HaveVerdict = v, true
				}
			}
		case strings.HasPrefix(low, "finding:"):
			last = -1
			read++
			if read > MaxFindings {
				out.Dropped++
				continue
			}
			f, ok := validFinding(line, d)
			if !ok {
				out.Dropped++
				continue
			}
			key := f.File + ":" + strconv.Itoa(f.Line) + ":" + f.Body
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Findings = append(out.Findings, f)
			last = len(out.Findings) - 1
		case strings.HasPrefix(low, "fix:") && last >= 0 && out.Findings[last].Fix == "":
			fix := sanitize.Line(line[len("fix:"):], 0)
			if fix != "" && len(fix) <= MaxFixBytes {
				out.Findings[last].Fix = fix
			}
		}
	}
	sort.SliceStable(out.Findings, func(i, j int) bool {
		a, b := out.Findings[i], out.Findings[j]
		if a.Severity.rank() != b.Severity.rank() {
			return a.Severity.rank() < b.Severity.rank()
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return out
}

// validFinding reads one finding line and checks it against the diff.
func validFinding(line string, d Diff) (Finding, bool) {
	m := findingLine.FindStringSubmatch(line)
	if m == nil {
		return Finding{}, false
	}
	file := strings.Trim(m[1], "`")
	file = strings.TrimPrefix(file, "./")
	n, err := strconv.Atoi(m[2])
	if err != nil || n < 1 || !PlainPath(file) || !d.HasLine(file, n) {
		return Finding{}, false
	}
	sev := Severity(strings.ToLower(m[3]))
	if sev.rank() < 0 {
		return Finding{}, false
	}
	rub := strings.ToLower(m[4])
	if !validRubric(rub) {
		return Finding{}, false
	}
	body := sanitize.Line(m[5], 0)
	if body == "" || len(body) > MaxBodyBytes {
		return Finding{}, false
	}
	return Finding{File: file, Line: n, Severity: sev, Rubric: rub, Body: body}, true
}
