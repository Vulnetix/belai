package components

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

	// Firewall reports whether traffic is routed through the Vulnetix AI
	// Firewall. Rendered as a teal chip when on; omitted when off.
	Firewall bool

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

	// Armed is a transient operator prompt (e.g. "press ctrl+d again to exit ·
	// esc cancels") rendered in place of Hint while a two-press key is armed.
	// Like Hint it occupies the same fixed line, so the footer height never
	// changes when it appears or clears.
	Armed string

	// Subagents is the subagent roster rendered on a dedicated line. The line
	// always renders (empty when the roster is empty) so the footer height stays
	// constant as chips appear and disappear. MainFocused reports whether the
	// [main] chip is the selected roster entry.
	Subagents   []SubagentChip
	MainFocused bool

	// Pulse is the followed agent's loop state. While a thread is filtered it
	// takes the roster's line, so the footer says what that agent is doing.
	Pulse *AgentPulse
}

// SubagentChip is one roster entry in the footer's subagent strip.
type SubagentChip struct {
	ID      string
	Label   string
	State   string
	Focused bool
	// Glyph is the state mark drawn inside the chip; Detail is the short
	// muted note after it (the running tool, the iteration). Both are
	// optional and Detail is the first thing dropped when space runs out.
	Glyph  string
	Detail string
}

// AgentPulse is one agent's loop state, drawn as a single footer line.
type AgentPulse struct {
	Glyph   string
	Label   string
	State   string
	Step    string // the running tool ("⚙ Grep") or "thinking"
	Iter    string // "iter 3/12"
	Tools   int
	Errors  int
	Elapsed string
	Last    string // the latest line of output
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
	hint := f.Hint
	if f.Armed != "" {
		hint = f.Armed
	}
	hint = ansi.Truncate(hint, f.Width, "")
	if line1 != "" {
		return rule + "\n" + line1 + "\n" + line2 + "\n" + f.subagentLine() + "\n" + MutedStyle.Render(hint)
	}
	return rule + "\n" + line2 + "\n" + f.subagentLine() + "\n" + MutedStyle.Render(hint)
}

// subagentLine renders the subagent roster as one line. It returns "" when
// the roster is empty (still emitting the dedicated footer line), otherwise
// [main] leads the strip, chips follow in insertion order, and overflow
// collapses to a "→ N more" marker matching the /model windowed-list idiom.
func (f *Footer) subagentLine() string {
	if f.Pulse != nil {
		return f.pulseLine()
	}
	if len(f.Subagents) == 0 {
		return ""
	}
	// Details first; when they do not all fit, the chips alone.
	if line, ok := f.chipLine(true); ok {
		return line
	}
	line, _ := f.chipLine(false)
	return line
}

// chipLine lays the roster out on one line. ok is false when a chip had to
// collapse into the "→ N more" marker.
func (f *Footer) chipLine(details bool) (string, bool) {
	parts := []string{renderSubagentChip(SubagentChip{ID: "", Label: "main", State: "main", Focused: f.MainFocused})}
	used := lipgloss.Width(parts[0])
	const sep = 2
	for i, c := range f.Subagents {
		rendered := renderSubagentChip(c)
		if details && c.Detail != "" {
			rendered += " " + MutedStyle.Render(c.Detail)
		}
		w := lipgloss.Width(rendered)
		if used+w+sep > f.Width {
			parts = append(parts, MutedStyle.Render(fmt.Sprintf("→ %d more", len(f.Subagents)-i)))
			return strings.Join(parts, MutedStyle.Render("  ")), false
		}
		parts = append(parts, rendered)
		used += w + sep
	}
	return strings.Join(parts, MutedStyle.Render("  ")), true
}

// pulseLine renders the followed agent: its chip, the loop counters, and as
// much of its latest output as fits before the way back to main.
func (f *Footer) pulseLine() string {
	p := f.Pulse
	label := p.Label
	if p.Glyph != "" {
		label = p.Glyph + " " + label
	}
	chip := Chip(label, chipColour("x", p.State))
	parts := []string{MutedStyle.Render(p.State)}
	if p.Step != "" {
		if strings.HasPrefix(p.Step, "⚙") {
			parts = append(parts, WarnStyle.Render(p.Step))
		} else {
			parts = append(parts, MutedStyle.Render(p.Step))
		}
	}
	if p.Iter != "" {
		parts = append(parts, MutedStyle.Render(p.Iter))
	}
	if p.Tools > 0 {
		parts = append(parts, MutedStyle.Render(countNoun(p.Tools, "tool")))
	}
	if p.Errors > 0 {
		parts = append(parts, DangerStyle.Render(countNoun(p.Errors, "error")))
	}
	if p.Elapsed != "" {
		parts = append(parts, MutedStyle.Render(p.Elapsed))
	}
	left := chip + "  " + strings.Join(parts, MutedStyle.Render(" · "))
	hint := MutedStyle.Render("esc main")
	if room := f.Width - lipgloss.Width(left) - lipgloss.Width(hint) - 6; p.Last != "" && room > 12 {
		left += MutedStyle.Render("  › " + truncateRunes(p.Last, room))
	}
	pad := f.Width - lipgloss.Width(left) - lipgloss.Width(hint)
	if pad < 2 {
		return ansi.Truncate(left, f.Width, "…")
	}
	return left + strings.Repeat(" ", pad) + hint
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// renderSubagentChip renders one roster chip with its state colour. queued is
// muted, running is teal, done is teal-soft with a check, cancelled/failed is
// amber, and the focused chip renders inverse.
func renderSubagentChip(c SubagentChip) string {
	label := c.Label
	switch {
	case c.Glyph != "":
		label = c.Glyph + " " + c.Label
	case c.ID != "" && c.State == "done":
		label = c.Label + " ✓"
	}
	chip := Chip(label, chipColour(c.ID, c.State))
	if c.Focused {
		chip = lipgloss.NewStyle().Reverse(true).Render(chip)
	}
	return chip
}

// chipColour is the roster colour for a state; the main chip ("" id) is
// always teal-soft.
func chipColour(id, state string) lipgloss.TerminalColor {
	if id == "" {
		return ColorTealSoft
	}
	switch state {
	case "running":
		return ColorTeal
	case "done":
		return ColorTealSoft
	case "cancelled", "failed", "stopped", "paused":
		return ColorAmber
	default:
		return ColorMuted
	}
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
		effort := f.Effort
		if effort == "" {
			effort = "default"
		}
		modelPart += MutedStyle.Render(" · " + effort)
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
// teal when on and red when off. The firewall chip renders only when on.
func (f Footer) permissionChips() string {
	if !f.Guardrails && !f.Ask && !f.Firewall {
		return Chip("YOLO", ColorAmber)
	}
	guardrails := Chip("guardrails: "+onOff(f.Guardrails), onOffColor(f.Guardrails))
	ask := Chip("ask: "+onOff(f.Ask), onOffColor(f.Ask))
	out := guardrails + MutedStyle.Render(" ") + ask
	if f.Firewall {
		out += MutedStyle.Render(" ") + Chip("firewall: on", ColorTeal)
	}
	return out
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
