package vulnetixcli

import (
	"regexp"
	"strings"
)

// markerOK matches OK/PASS markers from auth-status-style output.
var markerOK = regexp.MustCompile(`\[(?:OK|PASS)\]`)

// markerFail matches FAIL/WARN markers.
var markerFail = regexp.MustCompile(`\[(?:FAIL|WARN)\]`)

// sectionHeading matches an all-caps line starting at column 0, which the
// vulnetix CLI uses to delimit sections such as "AUTH STATE".
var sectionHeading = regexp.MustCompile(`^([A-Z][A-Z /-]+):?\s*$`)

// sections splits text into named sections by all-caps column-0 headings.
// Lines before the first heading are returned under the empty key.
func sections(text string) map[string]string {
	out := map[string]string{}
	var name string
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := sectionHeading.FindStringSubmatch(line); m != nil {
			if name != "" || b.Len() > 0 {
				out[name] = strings.TrimSpace(b.String())
			}
			name = strings.TrimSpace(m[1])
			b.Reset()
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if name != "" || b.Len() > 0 {
		out[name] = strings.TrimSpace(b.String())
	}
	return out
}

// markerLine records an OK/FAIL/WARN marker and its descriptive text.
type markerLine struct {
	OK   bool
	Text string
}

// parseMarkers extracts [OK]/[FAIL]/[WARN]-style marker lines from a block.
func parseMarkers(block string) []markerLine {
	var out []markerLine
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case markerOK.MatchString(line):
			out = append(out, markerLine{OK: true, Text: markerOK.ReplaceAllString(line, "")})
		case markerFail.MatchString(line):
			out = append(out, markerLine{OK: false, Text: markerFail.ReplaceAllString(line, "")})
		}
	}
	return out
}
