package components

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Footer shows session, context usage (number and progress bar), model with
// optional effort, provider, and mode status.
type Footer struct {
	Session  string
	Tokens   int
	Model    string
	Provider string
	Mode     string
	Width    int
	Cwd      string
	Branch   string

	// Context metering.
	ContextLimit int  // 0 when the model's window is unknown
	ContextStale bool // usage predates a compaction
	Estimated    bool // no provider usage anchor yet; Tokens is an estimate

	// Session naming.
	SessionName string
	ShowName    bool
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

// View renders the footer as two lines.
func (f *Footer) View() string {
	if f.Width <= 0 {
		f.Width = 80
	}

	var line1Parts []string
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

	modeChip := Chip(f.Mode, modeColor(f.Mode))
	parts := []string{}
	if f.Provider != "" {
		parts = append(parts, MutedStyle.Render(f.Provider))
	}
	if f.Model != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorCream).Render(f.Model))
	}
	rightParts := []string{}
	rightParts = append(rightParts, MutedStyle.Render(f.sessionSegment()))
	rightParts = append(rightParts, f.contextSegment())
	rightParts = append(rightParts, f.contextBar())

	left := strings.Join(parts, MutedStyle.Render(" · "))
	right := strings.Join(rightParts, MutedStyle.Render("  ·  "))

	line2 := left
	if right != "" {
		pad := f.Width - lipgloss.Width(left) - lipgloss.Width(right) - lipgloss.Width(modeChip) - 2
		if pad < 1 {
			pad = 1
		}
		line2 = left + strings.Repeat(" ", pad) + modeChip
		extraPad := f.Width - lipgloss.Width(line2) - lipgloss.Width(right)
		if extraPad < 1 {
			extraPad = 1
		}
		line2 = line2 + strings.Repeat(" ", extraPad) + right
	} else {
		line2 = left + " " + modeChip
	}

	rule := Rule(f.Width)
	if line1 != "" {
		return rule + "\n" + line1 + "\n" + line2
	}
	return rule + "\n" + line2
}

// sessionSegment renders the session name (when shown) or the short id.
func (f *Footer) sessionSegment() string {
	if f.ShowName && f.SessionName != "" {
		return "session: " + truncateRunes(f.SessionName, 24)
	}
	if f.Session != "" {
		return "session: " + f.Session
	}
	return ""
}

// contextSegment renders the context-window pressure with its three degraded
// renderings: a leading ~ for an estimate, (?) for a stale or unknown window,
// and a coloured remaining percentage only when it is safe to show one.
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
	if f.ContextStale || !f.Estimated {
		return fmt.Sprintf("tokens: %s (?)", tokens)
	}
	return fmt.Sprintf("tokens: ~%s (?)", tokens)
}

// barWidth is the cell width of the footer's context progress bar. Ten cells
// at eighth-cell resolution resolve in 2% steps, and the width fits the slot
// the old "cost:" label occupied.
const barWidth = 10

// barEighths renders a partially filled cell: entry i is (i+1)/8 full.
var barEighths = [8]rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// contextBar renders the context window as a fixed-width progress bar in the
// slot the cost label used to hold. Business rules:
//
//   - Fill is tokens/contextLimit clamped to [0, 1], rendered at
//     eighth-cell resolution so a 10-cell bar steps in 2% increments.
//   - Fill colour follows the same remaining-percentage thresholds as the
//     text segment (barColour), so bar and number can never disagree.
//   - When the window is unknown (ContextLimit == 0) or the usage predates a
//     compaction (ContextStale), the bar is empty and muted: the harness
//     draws no fill it cannot stand behind. The "(?)" lives in the text
//     segment.
//   - An unanchored (Estimated) token count fills the bar normally; the "~"
//     prefix in the text segment is what marks the estimate.
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
		full = int(cells) // floor for non-negative cells
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
			r = append(r, '░')
		}
	}
	if pct, ok := f.percentRemaining(); ok {
		return lipgloss.NewStyle().Foreground(f.barColour(pct)).Render(string(r))
	}
	return MutedStyle.Render(string(r))
}

// barColour applies the footer's pressure thresholds to a remaining
// percentage: <20% remaining reads red, <50% amber, otherwise teal. The text
// percentage and the bar share this one rule.
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

// formatTokens renders a token count compactly: 842, 12.4k, 1.2M.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		v := float64(n) / 1_000_000
		return trimFloat(v) + "M"
	case n >= 10_000:
		v := float64(n) / 1_000
		return trimFloat(v) + "k"
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
