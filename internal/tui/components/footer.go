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

	// RoutedModels, when > 0, reports that smart model routing is active over
	// that many distinct models. The provider/model/effort segment is then
	// replaced by the router label, because no single model serves the turn.
	RoutedModels int

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

	// Firewall reports whether traffic is routed through the active AI
	// Firewall. Rendered as a teal chip when on; omitted when off.
	Firewall bool
	// FirewallLabel names the active firewall (and its event count) on the
	// chip; empty renders "on".
	FirewallLabel string

	// RC is the remote-control daemon's segment ("rc 2": running, two
	// sessions); empty while `belai rc` is not running.
	RC string

	// Voice is the speech-input segment ("voice: listening"); empty while voice
	// input is off. VoiceOn draws the dot filled while the microphone is open
	// or the model is working.
	Voice   string
	VoiceOn bool

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

	// Review is a /vulnetix review in flight, drawn at the head of the roster
	// line so a review that is still scanning is visible from the main
	// thread. Nil when no review is running.
	Review *ReviewProgress

	// Budget is the token budget shown right-aligned on line 1: nil when the
	// routing is "routed" or the selected model has no budget. The app picks
	// which of the model's budgets to show as they cycle.
	Budget *BudgetGauge

	// Intel is the session intelligence slot shown right-aligned on line 1 when
	// no budget is (the cycle is on the intel slot, routing is routed, or the
	// selected model has no budget). Budget takes the line when both are set.
	Intel *IntelGauge
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

// ReviewProgress is a running /vulnetix review: how many of its activities
// (scanners plus the post-scan fix) finished, which are still running, and
// how many scanner agents are still grounding reports.
type ReviewProgress struct {
	Glyph   string
	Done    int
	Total   int
	Pending []string
	Agents  int
	Elapsed string
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

// View renders the footer as three lines under the composer, whose bottom
// edge is its separator: line 1 carries the mode, cwd and branch; line 2
// carries provider/model/effort and the safety switches on the left and
// session/context/bar on the right (SessionRow); line 3 is the hover or armed
// hint when there is one, and otherwise the subagent roster.
func (f *Footer) View() string {
	if f.Width <= 0 {
		f.Width = 80
	}

	// The mode is coloured text, not a chip: solid chips are reserved for a
	// relaxed safety switch (docs/tui-design.md).
	mode := lipgloss.NewStyle().Foreground(modeColor(f.Mode)).Bold(true).Render(f.Mode)
	if f.Agent != "" {
		mode += LowStyle.Render(" · " + f.Agent)
	}

	line1Parts := []string{mode}
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
	line1 := f.withBudget(strings.Join(line1Parts, LowStyle.Render("  ·  ")))

	left, pad, right, _, _, _ := f.line2Layout()
	line2 := left
	if right != "" {
		line2 = left + strings.Repeat(" ", pad) + right
	}

	return line1 + "\n" + line2 + "\n" + f.statusLine()
}

// SessionRow is the footer-internal line index of the session segment.
func (f *Footer) SessionRow() int { return 1 }

// statusLine is the footer's last line: an armed or hover hint takes it while
// one is showing, and the subagent roster (or review, or followed agent)
// holds it otherwise.
func (f *Footer) statusLine() string {
	hint := f.Hint
	if f.Armed != "" {
		hint = f.Armed
	}
	if hint != "" {
		return MutedStyle.Render(ansi.Truncate(hint, f.Width, ""))
	}
	return f.subagentLine()
}

// subagentLine renders the subagent roster as one line. It returns "" when
// the roster is empty (still emitting the dedicated footer line), otherwise
// [main] leads the strip, chips follow in insertion order, and overflow
// collapses to a "→ N more" marker matching the /model windowed-list idiom.
func (f *Footer) subagentLine() string {
	if f.Pulse != nil {
		return f.pulseLine()
	}
	review := f.reviewSegment()
	if len(f.Subagents) == 0 {
		return review
	}
	// The review leads and the roster gets what is left of the line.
	rest := *f
	if review != "" {
		rest.Width = f.Width - lipgloss.Width(review) - 2
		if rest.Width < 16 {
			return review
		}
	}
	// Details first; when they do not all fit, the chips alone.
	line, ok := rest.chipLine(true)
	if !ok {
		line, _ = rest.chipLine(false)
	}
	if review != "" {
		return review + "  " + line
	}
	return line
}

// reviewSegment renders the running review as a chip plus its progress,
// most useful facts first so truncation drops the least useful. Pending
// names past the third collapse to a count.
func (f *Footer) reviewSegment() string {
	r := f.Review
	if r == nil {
		return ""
	}
	label := fmt.Sprintf("vulnetix review %d/%d", r.Done, r.Total)
	if r.Glyph != "" {
		label = r.Glyph + " " + label
	}
	chip := Chip(label, ColorTeal)
	var parts []string
	if n := len(r.Pending); n > 0 {
		shown := r.Pending
		if n > 3 {
			shown = shown[:3]
		}
		s := strings.Join(shown, ", ")
		if n > 3 {
			s += fmt.Sprintf(" +%d", n-3)
		}
		parts = append(parts, s)
	}
	if r.Agents > 0 {
		parts = append(parts, countNoun(r.Agents, "agent"))
	}
	if r.Elapsed != "" {
		parts = append(parts, r.Elapsed)
	}
	line := chip
	if len(parts) > 0 {
		line += "  " + MutedStyle.Render(strings.Join(parts, " · "))
	}
	// Keep room for at least the [main] chip after the review.
	limit := f.Width
	if len(f.Subagents) > 0 {
		limit = f.Width * 2 / 3
	}
	return ansi.Truncate(line, limit, "…")
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
//
// The line never runs past the width. When it does not fit whole it sheds
// detail, least useful first: the provider name, then the firewall label,
// then the token counts (the remaining percentage stays), and only then is
// the left group truncated.
func (f *Footer) line2Layout() (left string, pad int, right string, sessionCol, sessionWidth int, sessionOK bool) {
	ctxBar := f.contextBar()
	sep := MutedStyle.Render("  ·  ")
	sepW := visibleLen("  ·  ")

	var ctxSeg string
	var budget int
	for level := 0; level <= line2ShedLevels; level++ {
		left = f.line2Left(level)
		ctxSeg = f.contextSegment()
		if level >= 3 {
			ctxSeg = f.contextSegmentShort()
		}
		budget = f.Width - visibleLen(left) - visibleLen(ctxSeg) - visibleLen(ctxBar) - 2*sepW - 1
		if budget >= minSessionBudget {
			break
		}
	}
	if budget < minSessionBudget {
		// Nothing left to shed: cut the left group so the right one fits.
		budget = minSessionBudget
		room := f.Width - budget - visibleLen(ctxSeg) - visibleLen(ctxBar) - 2*sepW - 1
		left = ansi.Truncate(left, max(room, 0), "…")
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

// line2ShedLevels is the last shedding level line2Layout tries; see line2Left.
const line2ShedLevels = 3

// minSessionBudget is the fewest cells the session segment is given
// ("session: " plus a few characters of the id).
const minSessionBudget = 12

// line2Left renders the left group of line 2 at a shedding level: 0 is in
// full, 1 drops the provider name, 2 also drops the firewall label (the
// switch itself stays). Level 3 sheds on the right side, in line2Layout.
func (f *Footer) line2Left(level int) string {
	parts := []string{}
	switch {
	case f.RoutedModels > 0:
		noun := "models"
		if f.RoutedModels == 1 {
			noun = "model"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorCream).Render("Smart model router")+
			MutedStyle.Render(fmt.Sprintf(" · %d %s active", f.RoutedModels, noun)))
	case f.Provider != "" && level < 1:
		parts = append(parts, MutedStyle.Render(f.Provider))
	}
	if f.Model != "" && f.RoutedModels == 0 {
		modelPart := lipgloss.NewStyle().Foreground(ColorCream).Render(f.Model)
		effort := f.Effort
		if effort == "" {
			effort = "default"
		}
		modelPart += MutedStyle.Render(" · " + effort)
		parts = append(parts, modelPart)
	}
	if chips := f.permissionChipsAt(level >= 2); chips != "" {
		parts = append(parts, chips)
	}
	parts = append(parts, f.cavemanSegment())
	return strings.Join(parts, MutedStyle.Render(" · "))
}

// contextSegmentShort is the context segment reduced to what matters most
// when the line is tight: the coloured remaining percentage, or the token
// count when no percentage is known.
func (f *Footer) contextSegmentShort() string {
	if pct, ok := f.percentRemaining(); ok {
		return f.colourPct(pct)
	}
	return f.contextSegment()
}

// SessionSpan reports the column range of the session segment on the footer's
// SessionRow, for mouse hit-testing. ok is false when no session segment
// renders.
func (f *Footer) SessionSpan() (col, width int, ok bool) {
	_, _, _, col, width, ok = f.line2Layout()
	return col, width, ok
}

// permissionChips renders the permission controls. Both off collapses to a
// single golden YOLO chip; otherwise guardrails and ask render as two chips,
// teal when on and red when off. The firewall chip renders only when on.
func (f Footer) permissionChips() string { return f.permissionChipsAt(false) }

// permissionChipsAt is permissionChips with the firewall's label dropped
// when bareFirewall is set, for a tight footer line.
func (f Footer) permissionChipsAt(bareFirewall bool) string {
	if !f.Guardrails && !f.Ask && !f.Firewall {
		return Chip("YOLO", ColorAmber)
	}
	// A switch that is on is a quiet dot; only a relaxed guardrail earns a
	// solid chip (docs/tui-design.md).
	guardrails := switchDot("guardrails", f.Guardrails)
	if !f.Guardrails {
		guardrails = Chip("guardrails: off", ColorDanger)
	}
	out := guardrails + "  " + switchDot("ask", f.Ask)
	if f.Firewall {
		label := "firewall"
		if f.FirewallLabel != "" && !bareFirewall {
			label += ": " + f.FirewallLabel
		}
		out += "  " + switchDot(label, true)
	}
	if f.Voice != "" {
		out += "  " + switchDot(f.Voice, f.VoiceOn)
	}
	if f.RC != "" {
		out += "  " + switchDot(f.RC, true)
	}
	return out
}

// switchDot renders a footer switch as `● label` (on, teal dot) or
// `○ label` (off, all low).
func switchDot(label string, on bool) string {
	if on {
		return AccentStyle.Render("●") + MutedStyle.Render(" "+label)
	}
	return LowStyle.Render("○ " + label)
}

// cavemanSegment renders the caveman voice-rewrite status. Unlike the
// permission chips it never collapses away: off is as much a fact as on, so
// both render, as a switch dot like the safety switches.
func (f Footer) cavemanSegment() string {
	return switchDot("caveman", f.Caveman)
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
	tokens := FormatTokens(f.Tokens)

	if f.ContextLimit > 0 {
		limit := FormatTokens(f.ContextLimit)
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
	frac := 0.0
	if f.ContextLimit > 0 && !f.ContextStale {
		frac = float64(f.Tokens) / float64(f.ContextLimit)
	}
	fill, trough := fillBar(frac, barWidth)
	if f.ContextLimit <= 0 {
		trough = strings.Repeat("·", barWidth-len([]rune(fill)))
	}
	bar := fill + trough
	if pct, ok := f.percentRemaining(); ok {
		return lipgloss.NewStyle().Foreground(f.barColour(pct)).Render(bar)
	}
	return MutedStyle.Render(bar)
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

// FormatTokens renders an integer token count as a compact human-readable
// string, e.g. 1234 -> "1.2k". It is used by both the footer and file cards.
func FormatTokens(n int) string {
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

// minBudgetLeft is the narrowest the left side of line 1 (mode chip, cwd,
// branch) is truncated to before the budget gauge gives up its percentage.
const minBudgetLeft = 20

// withBudget right-aligns the budget gauge on line 1. When the line is too
// narrow the gauge first drops the time left; then the left side (cwd and
// branch) is truncated, down to minBudgetLeft cells so the mode chip stays;
// only then does the gauge drop its percentage. The colour and the bar are
// always kept.
func (f *Footer) withBudget(left string) string {
	var segAt func(detail int) string
	switch {
	case f.Budget != nil:
		segAt = f.Budget.budgetSegment
	case f.Intel != nil:
		segAt = f.Intel.segmentAt
	default:
		return left
	}
	fits := func(l, seg string) bool { return lipgloss.Width(l)+1+lipgloss.Width(seg) <= f.Width }
	seg := segAt(2)
	if !fits(left, seg) {
		seg = segAt(1)
		if f.Width-lipgloss.Width(seg)-1 < min(minBudgetLeft, lipgloss.Width(left)) {
			seg = segAt(0)
		}
	}
	room := f.Width - lipgloss.Width(seg) - 1
	if room < 0 {
		return ansi.Truncate(seg, f.Width, "")
	}
	if lipgloss.Width(left) > room {
		left = ansi.Truncate(left, room, "…")
	}
	return left + strings.Repeat(" ", f.Width-lipgloss.Width(left)-lipgloss.Width(seg)) + seg
}
