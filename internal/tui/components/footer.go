package components

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Footer shows session, token/cost, model, provider, and mode status.
type Footer struct {
	Session  string
	Tokens   int
	Cost     string
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

var (
	modeStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
)

func modeColor(mode string) lipgloss.Color {
	switch mode {
	case "plan":
		return lipgloss.Color("4")
	case "goal":
		return lipgloss.Color("5")
	default:
		return lipgloss.Color("2")
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
		line1Parts = append(line1Parts, cwd)
	}
	if f.Branch != "" {
		line1Parts = append(line1Parts, "⎇ "+f.Branch)
	}
	line1 := strings.Join(line1Parts, "  ")

	modeChip := modeStyle.Background(modeColor(f.Mode)).Foreground(lipgloss.Color("0")).Render(f.Mode)
	parts := []string{}
	if f.Provider != "" {
		parts = append(parts, f.Provider)
	}
	if f.Model != "" {
		parts = append(parts, f.Model)
	}
	rightParts := []string{}
	rightParts = append(rightParts, f.sessionSegment())
	rightParts = append(rightParts, f.contextSegment())
	if f.Cost != "" {
		rightParts = append(rightParts, "cost: "+f.Cost)
	}

	left := strings.Join(parts, " · ")
	right := strings.Join(rightParts, "  |  ")

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

	if line1 != "" {
		return line1 + "\n" + line2
	}
	return line2
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
	colour := lipgloss.Color("2")
	switch {
	case pct < 20:
		colour = lipgloss.Color("1")
	case pct < 50:
		colour = lipgloss.Color("3")
	}
	return lipgloss.NewStyle().Foreground(colour).Render(fmt.Sprintf("%d%%", pct))
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
