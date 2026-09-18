package components

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Footer shows session, context usage (number and progress bar), model with
// optional effort, permission controls, the caveman voice status, provider,
// and mode status.
type Footer struct {
	Session string
	Tokens  int
	Model   string

	// Effort is the model's reasoning effort (e.g. "low", "medium", "high",
	// "none"). Rendered subtly next to the model when set; empty means the
	// provider default and renders nothing.
	Effort   string
	Provider string
	Mode     string

	// Agent is the engaged agent profile, shown inside the mode chip. Empty
	// means the default agent, which the mode name already says.
	Agent  string
	Width  int
	Cwd    string
	Branch string

	// Context metering.
	ContextLimit int  // 0 when the model's window is unknown
	ContextStale bool // usage predates a compaction
	Estimated    bool // no provider usage anchor yet; Tokens is an estimate

	// Guardrails reports whether the posture gates are at their defaults.
	// Ask reports whether the permission-ask gate is on. Both off renders one
	// golden YOLO chip; otherwise the two chips render individually.
	Guardrails bool
	Ask        bool

	// Caveman reports whether the caveman voice rewrite is active. It always
	// renders, on and off alike, because it silently changes how every reply
	// is written and the footer is the only place that says so.
	Caveman bool

	// Session naming.
	SessionName string
	ShowName    bool

	// Hint is the hover hint rendered on a dedicated third content line. It is
	// always emitted (empty when there is nothing to show) so the footer's
	// height never changes with the pointer, which would shift the viewport
	// under a stationary mouse. The caller styles it (HelpBar).
	Hint string
}

func modeColor(mode string) lipgloss.TerminalColor {
	switch mode {
	case "plan":
		return ColorTealSoft
	case "goal":
		return ColorAmber
	default:
		return ColorTeal
	}
}

// View renders the footer as three content lines plus the rule: line 1
// carries the mode chip, cwd and branch; line 2 carries provider/model/effort/
// permission controls on the left and session/context/bar on the right; line 3
// is the hover hint (empty unless the pointer is over an actionable region).
func (f *Footer) View() string {
	if f.Width <= 0 {
		f.Width = 80
	}

	modeLabel := f.Mode
	if f.Agent != "" {
		modeLabel += " · " + f.Agent
	}
	modeChip := Chip(modeLabel, modeColor(f.Mode))

	var line1Parts []string
	line1Parts = append(line1Parts, modeChip)
	if f.Cwd != "" {
		cwd := f.Cwd
		if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(cwd, home) {
			cwd = "~" + strings.TrimPrefix(cwd, home)
		}
		line1Parts = append(line1Parts, MutedStyle.Render(cwd))
	}
	if f.Branch != "" {
		line1Parts = append(line1Parts, AccentStyle.Render("⎇ ")+MutedStyle.Render(f.Branch))
	}
	line1 := strings.Join(line1Parts, MutedStyle.Render("  ·  "))

	left, pad, right, _, _, _ := f.line2Layout()
	line2 := left
	if right != "" {
		line2 = left + strings.Repeat(" ", pad) + right
	}

	rule := Rule(f.Width)
	if line1 != "" {
		return rule + "\n" + line1 + "\n" + line2 + "\n" + MutedStyle.Render(f.Hint)
	}
	return rule + "\n" + line2 + "\n" + MutedStyle.Render(f.Hint)
}

// line2Layout computes the footer's second content line. It returns the
// rendered left group, the padding inserted before the right group, the
// rendered right group, and the column range of the session segment within
// that line (for hover hit-testing; ok is false when no session segment
// renders). The session segment is always the first token of the right group,
// so its column is width(left)+pad and its width is the plain segment width.
func (f *Footer) line2Layout() (left string, pad int, right string, sessionCol, sessionWidth int, sessionOK bool) {
	parts := []string{}
	if f.Provider != "" {
		parts = append(parts, MutedStyle.Render(f.Provider))
	}
	if f.Model != "" {
		modelPart := lipgloss.NewStyle().Foreground(ColorCream).Render(f.Model)
		if f.Effort != "" {
			modelPart += MutedStyle.Render(" · " + f.Effort)
		}
		parts = append(parts, modelPart)
	}
	if chips := f.permissionChips(); chips != "" {
		parts = append(parts, chips)
	}
	parts = append(parts, f.cavemanSegment())
	left = strings.Join(parts, MutedStyle.Render(" · "))

	ctxSeg := f.contextSegment()
	ctxBar := f.contextBar()
	sep := MutedStyle.Render("  ·  ")
	sepW := visibleLen("  ·  ")

	budget := f.Width - visibleLen(left) - visibleLen(ctxSeg) - visibleLen(ctxBar) - 2*sepW - 1
	if budget < 12 {
		budget = 12
	}
	sessionPlain := f.sessionSegment(budget)

	rightParts := []string{}
	if sessionPlain != "" {
		rightParts = append(rightParts, MutedStyle.Render(sessionPlain))
		sessionOK = true
	}
	if ctxSeg != "" {
		rightParts = append(rightParts, ctxSeg)
	}
	rightParts = append(rightParts, ctxBar)
	right = strings.Join(rightParts, sep)

	pad = 1
	if right != "" {
		pad = f.Width - lipgloss.Width(left) - lipgloss.Width(right)
		if pad < 1 {
			pad = 1
		}
	}
	if sessionOK {
		sessionCol = lipgloss.Width(left) + pad
		sessionWidth = visibleLen(sessionPlain)
	}
	return left, pad, right, sessionCol, sessionWidth, sessionOK
}

// SessionSpan reports the column range of the session segment on the footer's
// second content line (footer-internal line index 2), for mouse hit-testing.
// ok is false when no session segment renders.
func (f *Footer) SessionSpan() (col, width int, ok bool) {
	_, _, _, col, width, ok = f.line2Layout()
	return col, width, ok
}

// permissionChips renders the permission controls. Both off collapses to a
// single golden YOLO chip; otherwise guardrails and ask render as two chips,
// teal when on and red when off.
func (f Footer) permissionChips() string {
	if !f.Guardrails && !f.Ask {
		return Chip("YOLO", ColorAmber)
	}
	guardrails := Chip("guardrails: "+onOff(f.Guardrails), onOffColor(f.Guardrails))
	ask := Chip("ask: "+onOff(f.Ask), onOffColor(f.Ask))
	return guardrails + MutedStyle.Render(" ") + ask
}

// cavemanSegment renders the caveman voice-rewrite status. Unlike the
// permission chips it never collapses away: off is as much a fact as on, so
// both render. The value is teal when on and muted when off, while the label
// stays muted either way so the safety chips keep the visual lead.
func (f Footer) cavemanSegment() string {
	if f.Caveman {
		return MutedStyle.Render("caveman: ") + lipgloss.NewStyle().Foreground(ColorTeal).Render("on")
	}
	return MutedStyle.Render("caveman: off")
}

func onOffColor(on bool) lipgloss.TerminalColor {
	if on {
		return ColorTeal
	}
	return ColorDanger
}

// sessionSegment renders the session name (when shown) or the short id,
// truncated to the budget rune-safely.
func (f *Footer) sessionSegment(budget int) string {
	if budget < 1 {
		budget = 12
	}
	if f.ShowName && f.SessionName != "" {
		return "session: " + truncateRunes(f.SessionName, budget-len("session: "))
	}
	if f.Session != "" {
		return "session: " + truncateRunes(f.Session, budget-len("session: "))
	}
	return ""
}

// contextSegment renders the context-window pressure with its three degraded
// renderings: a leading ~ for an estimate, (?) for a stale window, and a
// coloured remaining percentage only when it is safe to show one.
func (f *Footer) contextSegment() string {
	tokens := formatTokens(f.Tokens)

	if f.ContextLimit > 0 {
		limit := formatTokens(f.ContextLimit)
		if f.ContextStale {
			return fmt.Sprintf("tokens: ~%s/%s (?)", tokens, limit)
		}
		prefix := ""
		if f.Estimated {
			prefix = "~"
		}
		pct, ok := f.percentRemaining()
		if !ok {
			return fmt.Sprintf("tokens: %s%s/%s (?)", prefix, tokens, limit)
		}
		return fmt.Sprintf("tokens: %s%s/%s (%s)", prefix, tokens, limit, f.colourPct(pct))
	}
	if f.Estimated {
		return fmt.Sprintf("tokens: ~%s / unknown", tokens)
	}
	return fmt.Sprintf("tokens: %s / unknown", tokens)
}

const barWidth = 10

var barEighths = [8]rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// contextBar renders the context window as a fixed-width progress bar. An
// unknown window renders a dotted muted trough with no fill claim; a stale
// window renders an empty muted bar as before.
func (f *Footer) contextBar() string {
	w := barWidth
	full, eighth := 0, 0
	if f.ContextLimit > 0 && !f.ContextStale {
		frac := float64(f.Tokens) / float64(f.ContextLimit)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		cells := frac * float64(w)
		full = int(cells)
		eighth = int((cells-float64(full))*8 + 0.5)
		if eighth > 7 {
			full++
			eighth = 0
		}
	}
	r := make([]rune, 0, w)
	for i := 0; i < w; i++ {
		switch {
		case i < full:
			r = append(r, '█')
		case i == full && eighth > 0:
			r = append(r, barEighths[eighth-1])
		default:
			if f.ContextLimit <= 0 {
				r = append(r, '·')
			} else {
				r = append(r, '░')
			}
		}
	}
	if pct, ok := f.percentRemaining(); ok {
		return lipgloss.NewStyle().Foreground(f.barColour(pct)).Render(string(r))
	}
	return MutedStyle.Render(string(r))
}

func (f *Footer) barColour(remainingPct int) lipgloss.TerminalColor {
	switch {
	case remainingPct < 20:
		return ColorDanger
	case remainingPct < 50:
		return ColorAmber
	default:
		return ColorTeal
	}
}

func (f *Footer) percentRemaining() (int, bool) {
	if f.ContextLimit <= 0 || f.ContextStale {
		return 0, false
	}
	if f.Tokens >= f.ContextLimit {
		return 0, true
	}
	return int(float64(f.ContextLimit-f.Tokens) / float64(f.ContextLimit) * 100), true
}

func (f *Footer) colourPct(pct int) string {
	return lipgloss.NewStyle().Foreground(f.barColour(pct)).Render(fmt.Sprintf("%d%%", pct))
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return trimFloat(float64(n)/1_000_000) + "M"
	case n >= 10_000:
		return trimFloat(float64(n)/1_000) + "k"
	default:
		return fmt.Sprintf("%d", n)
	}
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(strings.TrimSuffix(s, "0"), ".")
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
