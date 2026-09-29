package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modeltest"
	"github.com/vulnetix/belai/internal/tui/components"
)

var modelTestSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// modelTestPanel renders the test of the last model selection: its steps
// while it runs, a download prompt when one is pending, and whether the
// selection was saved.
func (a *App) modelTestPanel(w int) string {
	run := a.modelTest
	if run == nil {
		return ""
	}
	var b strings.Builder
	title := "testing " + run.change.label
	switch run.outcome {
	case "saved":
		title = "tested " + run.change.label
	case "not-saved", "write-failed":
		title = "tested " + run.change.label
	case "cancelled":
		title = "test cancelled · " + run.change.label
	}
	b.WriteString(components.EmphStyle.Render(ansi.Truncate(title, w, "…")) + "\n")

	frame := a.modelTestFrame(run)
	for _, s := range run.steps {
		b.WriteString(a.modelTestStepLine(run, s, w, frame) + "\n")
		if s.status == modeltest.StatusRunning && run.dlTotal > 0 {
			b.WriteString("    " + a.downloadBar(run, w-4) + "\n")
		}
	}

	if o := run.confirm; o != nil {
		b.WriteString("\n" + components.WarnStyle.Render(ansi.Truncate(
			fmt.Sprintf("Download %s (%s) for %s?", o.What, modeltest.Sizes(o.Size), o.Label), w, "…")) + "\n")
		if o.Dest != "" {
			b.WriteString(components.MutedStyle.Render(ansi.Truncate("  into "+o.Dest, w, "…")) + "\n")
		}
		b.WriteString(components.HelpBar("y", "download", "n", "cancel") + "\n")
		return b.String()
	}

	switch run.outcome {
	case "running":
		b.WriteString(components.MutedStyle.Render("  nothing is saved until every check passes · esc cancels") + "\n")
	case "saved":
		b.WriteString(components.AccentStyle.Render(ansi.Truncate("✓ saved "+run.change.label+" → "+a.savedWhere(run.change), w, "…")) + "\n")
		if warns := countStatus(run.steps, modeltest.StatusWarn); warns > 0 {
			b.WriteString(components.WarnStyle.Render(fmt.Sprintf("  saved with %d warning%s above", warns, pluralS(warns))) + "\n")
		}
	case "not-saved":
		b.WriteString(components.DangerStyle.Render(ansi.Truncate("✗ not saved — "+run.change.was+" stays in effect", w, "…")) + "\n")
	case "write-failed":
		b.WriteString(components.DangerStyle.Render(ansi.Truncate("✗ the test passed but the settings file could not be written: "+run.note, w, "…")) + "\n")
	case "cancelled":
		b.WriteString(components.MutedStyle.Render("  cancelled; nothing was saved") + "\n")
	}
	if run.note != "" && run.outcome != "write-failed" {
		b.WriteString(components.MutedStyle.Render(ansi.Truncate("  "+run.note, w, "…")) + "\n")
	}
	if run.outcome != "running" && run.outcome != "saved" {
		b.WriteString(a.hintLines(run.hints, w))
	} else if run.outcome == "saved" && len(run.hints) > 0 {
		b.WriteString(a.hintLines(run.hints, w))
	}
	return b.String()
}

func (a *App) hintLines(hints []modeltest.Hint, w int) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, h := range hints {
		if seen[h.Text] {
			continue
		}
		seen[h.Text] = true
		key := "  → "
		if h.Key != "" {
			key = "  " + components.Chip(h.Key, components.ColorTealSoft) + " "
		}
		b.WriteString(key + components.MutedStyle.Render(ansi.Truncate(h.Text, w-6, "…")) + "\n")
	}
	return b.String()
}

// savedWhere names where a saved change lives.
func (a *App) savedWhere(ch stagedChange) string {
	switch ch.scope {
	case "session":
		return "this session only (not written to a file)"
	case "global":
		if p, err := config.SettingsPath(config.ScopeGlobal, a.workdir); err == nil {
			return p + " (global)"
		}
		return "global settings"
	}
	if p, err := config.SettingsPath(config.ScopeProject, a.workdir); err == nil {
		return p + " (project)"
	}
	return "project settings"
}

// downloadBar renders download progress with rate and time left.
func (a *App) downloadBar(run *modelTestRun, w int) string {
	done, total := run.dlDone, run.dlTotal
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
	}
	barW := max(10, min(30, w-40))
	fill := int(frac * float64(barW))
	bar := strings.Repeat("█", fill) + strings.Repeat("░", barW-fill)
	info := fmt.Sprintf(" %3.0f%%  %s / %s", frac*100, modeltest.Sizes(done), modeltest.Sizes(total))
	if el := time.Since(run.dlStart); el > time.Second && done > 0 {
		rate := float64(done) / el.Seconds()
		info += fmt.Sprintf("  %s/s", modeltest.Sizes(int64(rate)))
		if total > done && rate > 0 {
			info += "  " + fmtElapsed(time.Duration(float64(total-done)/rate*float64(time.Second))) + " left"
		}
	}
	return components.AccentStyle.Render(bar) + components.MutedStyle.Render(info)
}

func fmtElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func countStatus(steps []modelTestStepView, st modeltest.Status) int {
	n := 0
	for _, s := range steps {
		if s.status == st {
			n++
		}
	}
	return n
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func oneLineTUI(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// modelTestHelp is the help bar while the panel offers keys.
func (a *App) modelTestHelp() []string {
	run := a.modelTest
	if run == nil {
		return nil
	}
	if run.outcome == "running" {
		return []string{"esc", "cancel test"}
	}
	if run.outcome == "saved" {
		return nil
	}
	return []string{"r", "retry"}
}

// modelTestStepLine renders one step of the test: mark, name, detail, elapsed.
func (a *App) modelTestStepLine(run *modelTestRun, s modelTestStepView, w int, frame string) string {
	var mark string
	style := components.MutedStyle
	el := s.elapsed
	switch s.status {
	case modeltest.StatusOK:
		mark = components.AccentStyle.Render("✓")
	case modeltest.StatusWarn:
		mark, style = components.WarnStyle.Render("!"), components.WarnStyle
	case modeltest.StatusFail:
		mark, style = components.DangerStyle.Render("✗"), components.DangerStyle
	case modeltest.StatusSkip:
		mark = components.MutedStyle.Render("·")
	case modeltest.StatusRunning:
		mark = components.AccentStyle.Render(frame)
		el = time.Since(s.started)
	default:
		mark = components.MutedStyle.Render("·")
	}
	line := "  " + mark + " " + padRight(s.name, 22)
	detail := s.detail
	if s.status == modeltest.StatusRunning && run.logLine != "" && run.dlTotal == 0 {
		detail = run.logLine
	}
	if el > 0 {
		detail = strings.TrimSpace(detail + "  " + fmtElapsed(el))
	}
	room := w - ansi.StringWidth(line) - 1
	if room > 8 && detail != "" {
		line += " " + style.Render(ansi.Truncate(oneLineTUI(detail), room, "…"))
	}
	return line
}
