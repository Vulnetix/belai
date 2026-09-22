package lsp

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSourceRunes  = 24
	defaultMaxRows  = 10
	defaultMaxRunes = 200
)

// Render composes a human-readable, harness-shaped diagnostics block.
// It returns "" when there is nothing to report.
func Render(r Report, maxRows, maxRunes int) string {
	if len(r.Rows) == 0 {
		return ""
	}
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	if maxRunes <= 0 {
		maxRunes = defaultMaxRunes
	}

	rows := make([]Row, len(r.Rows))
	copy(rows, r.Rows)
	sortRows(rows)

	var b strings.Builder
	fmt.Fprintf(&b, "%s — ", r.Language)
	counts := countBySeverity(rows)
	parts := []string{}
	if counts[SeverityError] > 0 {
		parts = append(parts, fmt.Sprintf("%d error%s", counts[SeverityError], plural(counts[SeverityError])))
	}
	if counts[SeverityWarning] > 0 {
		parts = append(parts, fmt.Sprintf("%d warning%s", counts[SeverityWarning], plural(counts[SeverityWarning])))
	}
	if counts[SeverityInfo] > 0 {
		parts = append(parts, fmt.Sprintf("%d info", counts[SeverityInfo]))
	}
	if counts[SeverityHint] > 0 {
		parts = append(parts, fmt.Sprintf("%d hint", counts[SeverityHint]))
	}
	b.WriteString(strings.Join(parts, ", "))
	b.WriteByte('\n')

	truncated := 0
	if len(rows) > maxRows {
		truncated = len(rows) - maxRows
		rows = rows[:maxRows]
	}
	for _, row := range rows {
		msg := sanitizeMessage(row.Message, maxRunes)
		source := ""
		if row.Source != "" {
			source = sanitizeSource(row.Source)
			if source != "" {
				source = "  [" + source + "]"
			}
		}
		fmt.Fprintf(&b, "%d:%d\t%s\t%s%s\n", row.Line, row.Col, row.Severity.String(), msg, source)
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "… and %d more\n", truncated)
	}

	return strings.TrimRight(b.String(), "\n")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sortRows orders diagnostics: severity, then reference proximity, then
// line, then column. When no reference line is available the ordering falls
// back to line/column.
func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Severity != rows[j].Severity {
			return rows[i].Severity < rows[j].Severity
		}
		if rows[i].Line != rows[j].Line {
			return rows[i].Line < rows[j].Line
		}
		return rows[i].Col < rows[j].Col
	})
}

func countBySeverity(rows []Row) map[Severity]int {
	m := map[Severity]int{}
	for _, r := range rows {
		m[r.Severity]++
	}
	return m
}

// sanitizeMessage applies the hardening rules from the security analysis:
// strip control and format runes, flatten whitespace, cap at maxRunes.
func sanitizeMessage(s string, maxRunes int) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0 {
			continue
		}
		// Drop C0/C1 control and format characters (bidi, zero-width, etc).
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		// Flatten line and paragraph separators and tabs to spaces.
		switch r {
		case '\n', '\r', '\t', '\u2028', '\u2029':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	collapsed := strings.Join(strings.Fields(b.String()), " ")
	if collapsed == "" {
		return "(no message)"
	}
	runes := []rune(collapsed)
	if len(runes) > maxRunes {
		truncated := string(runes[:maxRunes-1])
		return truncated + "…"
	}
	if !utf8.ValidString(collapsed) {
		// Should be impossible after iterating runes, but keep the invariant.
		collapsed = strings.ToValidUTF8(collapsed, "")
	}
	return collapsed
}

// sanitizeSource restricts the diagnostic source to safe runes and caps it.
func sanitizeSource(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	runes := []rune(b.String())
	if len(runes) > maxSourceRunes {
		return string(runes[:maxSourceRunes-1]) + "…"
	}
	return b.String()
}
