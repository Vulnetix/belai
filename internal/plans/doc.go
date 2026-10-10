package plans

import (
	"errors"
	"regexp"
	"strings"
)

// Step is one numbered implementation step of a structured plan document.
type Step struct {
	N      int
	Text   string
	Files  []string
	Verify string
	// Notes are the step's other lines — sub-bullets and paragraphs that are
	// neither Files nor Verify — kept verbatim and rendered under the step.
	// Models put the substance of a step here (what to change, which
	// signature, which error to return); dropping them to the end of the
	// document, as an earlier version did, left every recorded plan with its
	// step bodies detached from their steps.
	Notes []string
}

// Doc is the structured plan document the harness parses, renders, reviews,
// and executes. It is deliberately tolerant: unrecognised sections and lines
// are preserved in Extra and rendered back out, so a model that adds its own
// headings never loses content. The only hard failure is a plan with zero
// numbered steps.
type Doc struct {
	Title       string
	Summary     string
	Steps       []Step
	Tests       []string
	Assumptions []string
	Risks       []string
	Extra       string
}

// headingRe matches a markdown heading and captures its text and level.
var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

// bulletRe matches a markdown bullet ("- text" or "* text").
var bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.+)$`)

// sectionFor normalises a heading to a document section. It returns "" for
// headings that do not map to a structured section, which then feed Extra.
func sectionFor(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	switch {
	case t == "summary", t == "overview", t == "context":
		return "summary"
	case t == "key changes", t == "steps", t == "implementation", t == "implementation plan", t == "changes", t == "execution sequence", t == "plan":
		return "steps"
	case t == "test plan", t == "tests", t == "testing", t == "verification":
		return "tests"
	case t == "assumptions", t == "assumptions and defaults", t == "decisions":
		return "assumptions"
	case t == "risks", t == "risk", t == "risks and mitigations":
		return "risks"
	}
	return ""
}

// stepField recognises the Files and Verify sub-bullets of a step in the
// spellings models actually produce ("Files:", "Files touched:", "File:",
// "Verify:", "Verification:", "Verify with:"). It returns the field and the
// value, or "" when the bullet is an ordinary note.
func stepField(sub string) (field, value string) {
	lower := strings.ToLower(sub)
	for _, p := range []string{"files touched:", "files changed:", "files:", "file:"} {
		if strings.HasPrefix(lower, p) {
			return "files", strings.TrimSpace(sub[len(p):])
		}
	}
	for _, p := range []string{"verify with:", "verification:", "verify:"} {
		if strings.HasPrefix(lower, p) {
			return "verify", strings.TrimSpace(sub[len(p):])
		}
	}
	// A bolded field name: "**Files:** a, b".
	if strings.HasPrefix(sub, "**") {
		if end := strings.Index(sub[2:], "**"); end > 0 {
			name := sub[2 : 2+end]
			rest := strings.TrimSpace(sub[2+end+2:])
			joined := name + " " + rest
			if strings.HasPrefix(rest, ":") {
				joined = name + rest
			}
			if f, v := stepField(joined); f != "" {
				return f, v
			}
		}
	}
	return "", ""
}

// ParseDoc parses markdown into a structured Doc. It returns an error only
// when zero numbered steps parse — the one property every executable plan must
// have. Everything else is recovered: unknown sections and non-step lines are
// carried in Extra, and fenced code blocks are skipped so numbered lines inside
// a code sample are never mistaken for steps.
func ParseDoc(md string) (Doc, error) {
	var d Doc
	var extra []string

	section := ""
	var curStep *Step
	// nested names the step field whose line had no value, so the bullets
	// nested deeper than nestedIndent under it are its value.
	nested := ""
	nestedIndent := 0

	flushStep := func() {
		if curStep != nil {
			d.Steps = append(d.Steps, *curStep)
			curStep = nil
		}
	}

	lines := strings.Split(md, "\n")
	inFence := false
	nextStep := 1
	nextStepLine := 0 // line index of the most recent step heading line

	for i, raw := range lines {
		line := strings.TrimSpace(raw)

		// Fenced code blocks are opaque: numbered lines inside them are code,
		// not plan steps.
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		// Title is the first level-1 heading; everything else is sectioned.
		if m := headingRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			title := strings.TrimSpace(m[2])
			if level == 1 && d.Title == "" {
				flushStep()
				d.Title = title
				section = ""
				continue
			}
			flushStep()
			if s := sectionFor(title); s != "" {
				section = s
			} else {
				section = ""
				extra = append(extra, line)
			}
			continue
		}

		if line == "" {
			continue
		}

		switch section {
		case "summary":
			if d.Summary == "" {
				d.Summary = strings.TrimSpace(raw)
			} else {
				d.Summary += "\n" + strings.TrimSpace(raw)
			}
		case "steps":
			if m := stepRe.FindStringSubmatch(line); m != nil {
				flushStep()
				nested = ""
				s := Step{N: nextStep, Text: strings.TrimSpace(m[1])}
				nextStep++
				nextStepLine = i
				curStep = &s
				continue
			}
			if curStep != nil {
				indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
				if m := bulletRe.FindStringSubmatch(line); m != nil {
					sub := strings.TrimSpace(m[1])
					// A bullet nested under an empty "Files:" or "Verify:" is
					// that field's value: models write the paths as a list.
					if nested != "" && indent > nestedIndent {
						if nested == "files" {
							// One bullet is one entry, however many commas its
							// description holds: '`a.ts` (new): export A, B, C'.
							curStep.Files = append(curStep.Files, sub)
						} else {
							curStep.Verify = joinVerify(curStep.Verify, sub)
						}
						continue
					}
					nested = ""
					switch field, value := stepField(sub); field {
					case "files":
						curStep.Files = append(curStep.Files, splitList(value)...)
						if strings.TrimSpace(value) == "" {
							nested, nestedIndent = "files", indent
						}
						continue
					case "verify":
						curStep.Verify = joinVerify(curStep.Verify, value)
						if strings.TrimSpace(value) == "" {
							nested, nestedIndent = "verify", indent
						}
						continue
					}
				} else {
					nested = ""
				}
				// Any other line under a step is the step's own detail and
				// stays with it.
				curStep.Notes = append(curStep.Notes, strings.TrimRight(raw, " \t"))
				continue
			}
			// A line in the steps section before the first step: preserve
			// verbatim.
			_ = nextStepLine
			extra = append(extra, raw)
		case "tests":
			if m := bulletRe.FindStringSubmatch(line); m != nil {
				d.Tests = append(d.Tests, strings.TrimSpace(m[1]))
			} else {
				d.Tests = append(d.Tests, line)
			}
		case "assumptions":
			if m := bulletRe.FindStringSubmatch(line); m != nil {
				d.Assumptions = append(d.Assumptions, strings.TrimSpace(m[1]))
			} else {
				d.Assumptions = append(d.Assumptions, line)
			}
		case "risks":
			if m := bulletRe.FindStringSubmatch(line); m != nil {
				d.Risks = append(d.Risks, strings.TrimSpace(m[1]))
			} else {
				d.Risks = append(d.Risks, line)
			}
		default:
			extra = append(extra, raw)
		}
	}
	flushStep()

	d.Extra = strings.TrimSpace(strings.Join(extra, "\n"))
	if len(d.Steps) == 0 {
		return Doc{}, errors.New("plan has no numbered steps")
	}
	return d, nil
}

// joinVerify adds a Verify value to what the step already has. Two Verify
// values are kept, joined by "; ", rather than one overwriting the other; an
// empty value adds nothing.
func joinVerify(cur, v string) string {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return cur
	case cur == "":
		return v
	}
	return cur + "; " + v
}

// splitList splits a comma-separated "Files:" value into trimmed entries.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Render returns the canonical markdown the harness writes to disk, so every
// recorded plan has one stable shape regardless of how the model formatted it.
func (d Doc) Render() string {
	var b strings.Builder
	if d.Title != "" {
		b.WriteString("# " + d.Title + "\n\n")
	}
	if d.Summary != "" {
		b.WriteString("## Summary\n\n" + d.Summary + "\n\n")
	}
	if len(d.Steps) > 0 {
		b.WriteString("## Steps\n\n")
		for _, s := range d.Steps {
			b.WriteString(itoa(s.N) + ". " + s.Text + "\n")
			for _, n := range s.Notes {
				if strings.TrimSpace(n) == "" {
					continue
				}
				if !strings.HasPrefix(n, " ") && !strings.HasPrefix(n, "\t") {
					n = "   " + n
				}
				b.WriteString(n + "\n")
			}
			for _, f := range s.Files {
				b.WriteString("   - Files: " + f + "\n")
			}
			if s.Verify != "" {
				b.WriteString("   - Verify: " + s.Verify + "\n")
			}
		}
		b.WriteString("\n")
	}
	if len(d.Tests) > 0 {
		b.WriteString("## Test Plan\n\n")
		for _, t := range d.Tests {
			b.WriteString("- " + t + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.Assumptions) > 0 {
		b.WriteString("## Assumptions\n\n")
		for _, a := range d.Assumptions {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.Risks) > 0 {
		b.WriteString("## Risks\n\n")
		for _, r := range d.Risks {
			b.WriteString("- " + r + "\n")
		}
		b.WriteString("\n")
	}
	if d.Extra != "" {
		b.WriteString(d.Extra + "\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
