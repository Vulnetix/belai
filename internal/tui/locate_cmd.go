package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/locate"
)

// locateReportMsg carries the result of /locate --dry-run.
type locateReportMsg struct{ text string }

// locateDryRunBudget bounds the walk a dry run makes.
const locateDryRunBudget = 6 * time.Second

// locateCommand handles /locate. With --dry-run it lists what explore locate
// would look at and where its questions would go, making no request.
func (a *App) locateCommand(arg string) tea.Cmd {
	if strings.TrimSpace(arg) != "--dry-run" {
		a.addSystem("usage: /locate --dry-run  (lists the files explore locate may look at and where it would send its questions; nothing is sent)")
		return nil
	}
	workdir := a.workdir
	dest, previews := a.settings.LocateDestination()
	on := a.settings.JevJobEnabled(config.JevExploreLocate)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), locateDryRunBudget)
		defer cancel()
		inv, err := locate.Build(ctx, workdir)
		if inv == nil {
			return locateReportMsg{text: "locate: " + err.Error()}
		}
		var b strings.Builder
		b.WriteString(locate.DryRun(inv))
		b.WriteString("\n")
		switch {
		case dest == "":
			b.WriteString("explore locate is off: no decision backend is set as the classifier")
		case !on:
			b.WriteString("explore locate is switched off (jev.jobs.explore_locate); it would send to " + dest)
		case previews:
			b.WriteString("questions would go to " + dest + ", with file paths, sizes and declared names")
		default:
			b.WriteString("questions would go to " + dest + ", with file paths and sizes only")
		}
		return locateReportMsg{text: b.String()}
	}
}
