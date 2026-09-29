// Command shot renders Belai's TUI surfaces headlessly and writes them as
// TrueColor ANSI captures for the marketing site. It imports
// internal/tui/components (never internal/tui) so it can reuse the exact
// theme palette and panel primitives the real TUI draws with, without driving
// the interactive App.
//
// Output is deterministic and diffable: no timestamps, no random values, no
// terminal-size sniffing. ansi-to-svg.mjs converts each capture to Geist-Mono
// SVG using the theme.go palette, so a regenerated capture is byte-stable on a
// clean tree — the property that lets CI treat `just shots` as a check.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	"github.com/muesli/termenv"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/tui/components"
)

const width = 100

func main() {
	outDir := flag.String("out", "site/src/assets/shots", "directory to write .ansi captures into")
	flag.Parse()

	// Force TrueColor so lipgloss never degrades to 256/16-colour, matching
	// what a modern terminal shows and keeping the capture palette-exact.
	lipgloss.SetColorProfile(termenv.TrueColor)
	// The TUI is a dark-terminal app (cream text on ink). Dark-background mode
	// resolves AdaptiveColor to its Dark variant, and the SVG converter draws
	// each capture on an ink background to match.
	lipgloss.SetHasDarkBackground(true)

	// Banner and ExitCard probe termenv.NewOutput(nil).ColorProfile(), which
	// reports Ascii unless stdout is a real terminal. Attach stdout to a pty
	// slave for the duration of rendering so that probe sees a TrueColor
	// terminal, then restore it. CI must be unset: termenv short-circuits any
	// CI environment to Ascii regardless of the tty.
	restore := forceTTY()
	defer restore()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	frames := map[string]string{
		"banner-composer": bannerComposer(),
		"footer":          footer(),
		"agent-turn":      agentTurn(),
		"plan-review":     planReview(),
		"approval-diff":   approvalDiff(),
		"settings":        settings(),
		"budgets":         budgets(),
		"intel":           intel(),
		"footer-intel":    footerIntel(),
		"permissions":     permissions(),
		"agents-roster":   agentsRoster(),
		"model-picker":    modelPicker(),
		"classifier":      classifier(),
		"local-model":     localModel(),
		"exit-card":       exitCard(),
		"kanban-pane":     kanbanPane(),
		"kanban-board":    kanbanBoard(),
	}

	// Restore stdout before any real output: the pty slave has no reader and
	// the captures are written to files, not printed.
	restore()

	for name, ansi := range frames {
		path := filepath.Join(*outDir, name+".ansi")
		if err := os.WriteFile(path, []byte(ansi), 0o644); err != nil {
			log.Fatalf("write %s: %v", path, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(ansi))
	}
}

// forceTTY re-points os.Stdout at a pty slave and arranges the environment so
// termenv reports TrueColor. It returns a func that restores the original
// stdout and environment.
func forceTTY() func() {
	master, slave, err := pty.Open()
	if err != nil {
		log.Fatalf("open pty: %v", err)
	}

	origStdout := os.Stdout
	os.Stdout = slave

	keys := []string{"TERM", "COLORTERM", "CI", "NO_COLOR"}
	restore := make(map[string]struct {
		val string
		ok  bool
	}, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		restore[k] = struct {
			val string
			ok  bool
		}{v, ok}
	}
	os.Setenv("TERM", "xterm-kitty")
	os.Setenv("COLORTERM", "truecolor")
	os.Unsetenv("CI")
	os.Unsetenv("NO_COLOR")

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		os.Stdout = origStdout
		for k, ev := range restore {
			if ev.ok {
				os.Setenv(k, ev.val)
			} else {
				os.Unsetenv(k)
			}
		}
		_ = slave.Close()
		_ = master.Close()
	}
}

// bannerComposer renders the boot banner and a populated composer line.
func bannerComposer() string {
	b := components.Banner{
		Width:   width,
		Version: "0.39.16",
		Commit:  "9084f16",
		Built:   "2025-09-20",
		// Pinned so the capture does not depend on the host's toolchain.
		Toolchain: "go1.25.1 linux/amd64",
		Tip:       "type " + components.KeyStyle.Render("/help") + components.MutedStyle.Render(" for commands and shortcuts"),
	}
	e := components.NewEditor()
	e.SetWidth(width)
	e.SetHeight(1)
	e.SetValue("write a /goal for the security classifier")
	e.CursorEnd()

	return b.View() + "\n\n" + composerChrome(e.Value())
}

func composerChrome(value string) string {
	return components.AccentStyle.Render("▸ ") + components.EmphStyle.Render(value)
}

// footer renders the three-line status footer with chips.
func footer() string {
	f := &components.Footer{
		Session:       "turn 14",
		Tokens:        12480,
		ContextLimit:  200000,
		Model:         "claude-sonnet-4-5",
		Effort:        "high",
		Provider:      "anthropic",
		Mode:          "agent",
		Agent:         "default",
		Width:         width,
		Cwd:           "/home/chris/GitHub/belai",
		Branch:        "main",
		Guardrails:    true,
		Ask:           true,
		Firewall:      true,
		FirewallLabel: "vulnetix",
		Caveman:       false,
		SessionName:   "seal-demo",
		ShowName:      true,
		Hint:          "hover for session details · ctrl+r reasoning · ctrl+t tools",
		Subagents: []components.SubagentChip{
			{ID: "f8-1", Label: "reader", State: "done"},
			{ID: "f8-2", Label: "scanner", State: "running"},
			{ID: "f8-3", Label: "docs", State: "queued"},
		},
		MainFocused: true,
		// 09:30: 60% of the day left but only 38% of the tokens, so amber.
		Budget: &components.BudgetGauge{Scope: "day", TokenPct: 38, UsedFrac: 0.62, TimeLeft: "14h 30m", State: components.BudgetAmber},
	}
	return f.View()
}

// agentTurn renders a streaming assistant turn with a collapsed Bash preview.
func agentTurn() string {
	assistant := components.Message{
		Role: "assistant",
		Content: "The Bash result came back clean, so I verified the delimiter " +
			"markup is stripped before anything reaches a system block. Here is " +
			"what changed:",
		ToolCalls: []components.AgentToolCall{
			{ID: "call_1", Name: "Bash", Args: `{"command":"gofmt -l . && go test ./internal/tools"}`},
		},
	}

	toolResult := components.Message{
		Role:       "tool",
		ToolName:   "Bash",
		Status:     "✓",
		Content:    "internal/tools/read.go\nok  \tgithub.com/vulnetix/belai/internal/tools\t1.2s",
		ToolCallID: "call_1",
	}

	return components.MessageList{
		Messages:  []components.Message{assistant, toolResult},
		Width:     width,
		ShowTools: true,
		ShowEdits: true,
	}.View()
}

// planReview renders the plan review pane with Approve / Refine / Cancel.
func planReview() string {
	doc := "Refactor the classifier\n" +
		"──────────────────────────\n" +
		"Split the classifier into a lexer and a grammar.\n\n" +
		"1  Extract a tokeniser module\n" +
		"   files: parser/lex.go, parser/lex_test.go\n" +
		"   verify: go test ./parser\n\n" +
		"2  Rewrite the grammar in terms of tokens\n" +
		"   files: parser/grammar.go\n" +
		"   verify: go test ./parser\n\n" +
		"tests        go test ./... · go vet ./...\n" +
		"assumptions  The token vocabulary is stable.\n" +
		"risks        Error messages may change."

	head := components.SectionHeader("plan review", "golden-plan · /tmp/golden-plan.md", width)
	body := components.Panel{Title: "plan", Width: width, Body: doc}.View()
	actions := components.Chip("approve", components.ColorTeal) + "  " +
		components.Chip("refine", components.ColorAmber) + "  " +
		components.Chip("cancel", components.ColorDanger)

	return head + body + "\n" + actions + "\n\n" +
		components.HelpBar("enter", "approve", "e", "refine", "esc", "cancel", "d", "diff")
}

// approvalDiff renders the tool-permission approval prompt showing a diff.
func approvalDiff() string {
	old := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	new := "package main\n\nfunc main() {\n\tprintln(\"hello, sealed\")\n}\n"
	ch := filediff.Preview("cmd/belai/main.go", old, new)

	head := components.SectionHeader("approve edit?", "Write · cmd/belai/main.go", width)
	diff := components.DiffView(&ch, width)
	prompt := components.Panel{
		Title: "diff",
		Width: width,
		Body:  diff,
		Raw:   true,
	}.View()
	actions := components.Chip("approve", components.ColorTeal) + "  " +
		components.Chip("deny", components.ColorDanger) + "  " +
		components.Chip("view full diff", components.ColorAmber)

	return head + prompt + "\n" + actions + "\n\n" +
		components.HelpBar("y", "approve", "n", "deny", "d", "diff")
}

// settings renders the /settings view with provenance.
func settings() string {
	head := components.SectionHeader("settings", "source · file", width)
	var rows []string
	rows = append(rows,
		kv("guardrails", "on", "config file"),
		kv("permission ask", "on", "config file"),
		kv("reasoning", "show", "config file"),
		kv("tool calls", "show", "config file"),
		kv("context limit", "200000", "model"),
		kv("effort", "high", "agent profile"),
		kv("voice", "default", "config file"),
	)
	rows = append(rows, components.Rule(width))
	rows = append(rows, components.MutedStyle.Render("every value shows its provenance: who set it, where, and when."))
	return head + strings.Join(rows, "\n") + "\n\n" +
		components.HelpBar("↑/↓", "select", "enter", "edit", "esc", "back")
}

// permissions renders the /permissions view.
func permissions() string {
	head := components.SectionHeader("permissions", "allow / deny / ask", width)
	var rows []string
	rows = append(rows, perm("Bash", "ask", components.ColorAmber))
	rows = append(rows, perm("Write", "ask", components.ColorAmber))
	rows = append(rows, perm("Edit", "ask", components.ColorAmber))
	rows = append(rows, perm("Read", "allow", components.ColorTeal))
	rows = append(rows, perm("Grep", "allow", components.ColorTeal))
	rows = append(rows, perm("Glob", "allow", components.ColorTeal))
	rows = append(rows, perm("WebFetch", "deny", components.ColorDanger))
	rows = append(rows, components.Rule(width))
	rows = append(rows, components.MutedStyle.Render("the surface is a union of policy, session, tool kind and role. Provenance is kept per grant."))
	return head + strings.Join(rows, "\n") + "\n\n" +
		components.HelpBar("↑/↓", "select", "tab", "cycle", "esc", "back")
}

// agentsRoster renders the f8 subagent roster and runs panel.
func agentsRoster() string {
	head := components.SectionHeader("agents", "f8 · runs panel", width)
	var rows []string
	rows = append(rows, agentRow("main", "streaming", components.ColorTeal, true))
	rows = append(rows, agentRow("reader", "done", components.ColorTealSoft, false))
	rows = append(rows, agentRow("scanner", "running · 12s", components.ColorTeal, false))
	rows = append(rows, agentRow("docs", "queued", components.ColorMuted, false))
	rows = append(rows, agentRow("composer", "cancelled", components.ColorAmber, false))
	rows = append(rows, components.Rule(width))
	rows = append(rows, components.MutedStyle.Render("runs are read-only fan-out: each branch sees only the surface it is allowed to touch."))
	return head + strings.Join(rows, "\n") + "\n\n" +
		components.HelpBar("f8", "roster", "↑/↓", "select", "enter", "runs output", "esc", "back")
}

// modelPicker renders the /model picker list.
func modelPicker() string {
	head := components.SectionHeader("model", "provider · anthropic", width)
	var rows []string
	rows = append(rows, modelRow("claude-sonnet-4-5", "high · 200k context", true))
	rows = append(rows, modelRow("claude-haiku-4-5", "low · 200k context", false))
	rows = append(rows, modelRow("llama-3.3-70b", "local · ollama", false))
	rows = append(rows, modelRow("qwen3-32b", "local · ollama", false))
	rows = append(rows, components.Rule(width))
	rows = append(rows, components.MutedStyle.Render("two local providers ship in the box, no cloud required."))
	return head + strings.Join(rows, "\n") + "\n\n" +
		components.HelpBar("↑/↓", "select", "enter", "choose", "esc", "back")
}

// classifier renders the /model classifier rows for the three-phase ML stack:
// the kind row, the two local phase gates, and the derived phase-3 status row.
func classifier() string {
	head := components.SectionHeader("model", "role · classifier", width)
	var rows []string
	rows = append(rows, classifierRow("kind", "models", "embedded · locked", components.ColorTeal))
	rows = append(rows, classifierRow("phase 1", "GuardrailsAI/prompt-saturation-attack-detector", "embedded", components.ColorTeal))
	rows = append(rows, classifierRow("phase 2", "jackhhao/jailbreak-classifier", "disabled", components.ColorMuted))
	rows = append(rows, classifierRow("phase 3", "off: set classifier provider + model to enable", "extraction only", components.ColorAmber))
	rows = append(rows, components.Rule(width))
	rows = append(rows, components.MutedStyle.Render("phases 1 and 2 run in-process over the same token windows; phase 3 is opt-in and adds nothing else."))
	return head + strings.Join(rows, "\n") + "\n\n" +
		components.HelpBar("↑/↓", "select", "tab", "cycle", "esc", "back")
}

// localModel renders a /local-model assess verdict.
func localModel() string {
	head := components.SectionHeader("local model", "/local-model · assess", width)
	body := components.Panel{
		Title: "verdict",
		Width: width,
		Body: "candidate   llama-3.3-70b (ollama)\n" +
			"fit         strong\n" +
			"notes       kept within 10% of the hosted baseline on the\n" +
			"            classifier probe; slower first-token on CPU.",
	}.View()
	return head + body + "\n\n" +
		components.HelpBar("enter", "accept", "r", "re-run", "esc", "back")
}

// exitCard renders the branded session-end card.
func exitCard() string {
	c := components.ExitCard{
		Name:      "seal-demo",
		SessionID: "a1b2c3d4e5f6a7b8",
		ResumeArg: "seal-demo",
		Turns:     14,
		Duration:  "9m 41s",
		Tokens:    "48,102",
		Model:     "claude-sonnet-4-5",
		Provider:  "anthropic",
		Path:      "/home/chris/GitHub/belai",
		Width:     width,
	}
	return c.View()
}

// kv renders a settings row as key · value · provenance.
func kv(key, value, source string) string {
	return components.MutedStyle.Render(key) + components.MutedStyle.Render("  ") +
		components.EmphStyle.Render(value) + components.MutedStyle.Render("  ·  source: "+source)
}

// perm renders one permission row.
func perm(name, state string, colour lipgloss.TerminalColor) string {
	return components.Cursor(false) + components.MutedStyle.Render(name) +
		components.MutedStyle.Render("  ") + components.Chip(state, colour)
}

// agentRow renders one subagent roster row with a selected marker.
func agentRow(name, state string, colour lipgloss.TerminalColor, selected bool) string {
	return components.Cursor(selected) + components.EmphStyle.Render(name) +
		components.MutedStyle.Render("  ") + components.Chip(state, colour)
}

// classifierRow renders one /model classifier row as label · value · state chip.
func classifierRow(label, value, state string, colour lipgloss.TerminalColor) string {
	return components.MutedStyle.Render(label) + components.MutedStyle.Render("  ") +
		components.EmphStyle.Render(value) + components.MutedStyle.Render("  ") + components.Chip(state, colour)
}

// modelRow renders one model picker row with a selected marker.
func modelRow(name, meta string, selected bool) string {
	return components.Cursor(selected) + components.EmphStyle.Render(name) +
		components.MutedStyle.Render("  ") + components.MutedStyle.Render(meta)
}

// budgets renders the token budgets screen (f1 then b): the selected model's
// budgets first, each row coloured by its state, with its bar. The clock is 09:30
// on day 24 of a 30-day month, so every row obeys the colour rules: the day is
// amber (60% of the day left, 38% of the tokens), the month red (exhausted).

func budgets() string {
	head := components.SectionHeader("Token budgets", "esc back", width)
	chip := components.Chip("global", components.ColorTealSoft) + "  " +
		components.MutedStyle.Render("~/.vulnetix/belai/settings.json") + "\n\n"
	row := func(selected bool, scope, limit, used string, g components.BudgetGauge, timeLeft string) string {
		style := lipgloss.NewStyle().Foreground(g.Colour())
		label := fmt.Sprintf("%-8s", scope)
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
		}
		return components.Cursor(selected) + label + fmt.Sprintf(" %9s limit  %9s used  ", limit, used) +
			style.Render(fmt.Sprintf("%3d%% left", g.TokenPct)) + fmt.Sprintf("  %-14s ", timeLeft) + g.Bar()
	}
	lines := []string{
		components.EmphStyle.Render("anthropic/claude-sonnet-4-5") + components.MutedStyle.Render("  (selected)"),
		row(true, "session", "2M", "420k", components.BudgetGauge{TokenPct: 79, UsedFrac: 0.21, State: components.BudgetTeal}, "—"),
		row(false, "day", "20M", "12.4M", components.BudgetGauge{TokenPct: 38, UsedFrac: 0.62, State: components.BudgetAmber}, "14h 30m (60%)"),
		row(false, "month", "300M", "300M", components.BudgetGauge{TokenPct: 0, UsedFrac: 1, State: components.BudgetRed}, "6d 3h (20%)"),
		"",
		components.EmphStyle.Render("openrouter/deepseek/deepseek-v4-flash"),
		row(false, "day", "5M", "600k", components.BudgetGauge{TokenPct: 88, UsedFrac: 0.12, State: components.BudgetTeal}, "14h 30m (60%)"),
		"",
		components.MutedStyle.Render("footer cycles every 10s · warnings on · change both in /settings"),
	}
	return head + chip + strings.Join(lines, "\n") + "\n\n" +
		components.HelpBar("↑↓", "move", "a", "add", "enter", "edit", "x", "delete", "esc", "back")
}

// kanbanColour mirrors internal/tui's kanbanListColor, so a shot shows each
// list in the colour the TUI draws it.
func kanbanColour(list string) lipgloss.TerminalColor {
	switch list {
	case "review":
		return components.ColorAmber
	case "blocked":
		return components.ColorDanger
	case "in progress":
		return components.ColorTeal
	case "done":
		return components.ColorTealSoft
	}
	return components.ColorMuted
}

// kanbanPane renders the kanban pane above an idle, empty composer, with the
// pane focused on its first row.
func kanbanPane() string {
	chips := components.Chip("all", components.ColorTealSoft) + " " +
		components.MutedStyle.Render("backlog") + " " + components.MutedStyle.Render("review") + " " +
		components.MutedStyle.Render("blocked")
	head := components.MutedStyle.Render("▤ kanban  ") + chips + components.MutedStyle.Render("  · this project") +
		components.MutedStyle.Render("   ↑↓ select · tab list · p scope · ⏎ use · esc back")
	row := func(selected bool, list, id, title string) string {
		prefix := "  "
		if selected {
			prefix = components.AccentStyle.Render("▸ ")
			title = components.EmphStyle.Render(title)
		}
		return prefix + components.Chip(list, kanbanColour(list)) + " " + components.MutedStyle.Render(id) + " " + title
	}
	pane := strings.Join([]string{
		head,
		row(true, "review", "K-3c0bcb", "Run the race detector over internal/kanban sync and fix what it finds"),
		row(false, "blocked", "K-5c5df2", "Waiting on the AWS profile for the kiro e2e"),
		row(false, "backlog", "K-c25071", "Document the wrap-up trigger catalogue in the README"),
		components.MutedStyle.Render("  +2 more"),
	}, "\n")
	composer := components.Panel{
		Title: "ask",
		Meta:  "⏎ send · ctrl+j newline · / commands · @ files · f1 screens",
		Width: width,
		Body:  components.MutedStyle.Render("Type / for commands, @ for files, or ask Belai anything…"),
	}.View()
	return pane + "\n" + composer
}

// kanbanBoard renders /kanban on the review list, with the selected item's
// provenance and history.
func kanbanBoard() string {
	head := components.SectionHeader("Kanban", "esc back", width)
	lists := []struct {
		name string
		n    int
	}{{"backlog", 3}, {"review", 2}, {"in progress", 1}, {"blocked", 1}, {"done", 14}}
	var tabs []string
	for _, l := range lists {
		label := fmt.Sprintf("%s %d", l.name, l.n)
		if l.name == "review" {
			tabs = append(tabs, components.Chip(label, kanbanColour(l.name)))
			continue
		}
		tabs = append(tabs, lipgloss.NewStyle().Foreground(kanbanColour(l.name)).Render(label))
	}
	meta := components.MutedStyle.Render("this project (belai) · synced 4s ago")
	rows := []string{
		components.Cursor(true) + components.MutedStyle.Render("K-3c0bcb ") + components.EmphStyle.Render("Run the race detector over internal/kanban sync and fix what it finds"),
		components.Cursor(false) + components.MutedStyle.Render("K-9e41d7 ") + "Update docs/session-sync.md for the kanban routes",
	}
	detail := []string{
		components.Chip("review", components.ColorAmber) + " " + components.EmphStyle.Render("Run the race detector over internal/kanban sync and fix what it finds"),
		components.MutedStyle.Render("belai · ~/src/belai · session 785d5499 · added 2026-09-27 19:11"),
		"  sync.go was only tested with a fake remote; go test -race was not run.",
		"  category: unverified",
		components.MutedStyle.Render("  09-27 19:11 added to review"),
		components.MutedStyle.Render("  09-27 19:40 note: reproduced a data race in Syncer.note"),
	}
	return head + strings.Join(tabs, "  ") + "\n" + meta + "\n\n" + strings.Join(rows, "\n") + "\n\n" +
		strings.Join(detail, "\n") + "\n\n" +
		components.HelpBar("←→", "list", "↑↓", "item", "⏎", "work on it", "n", "new", "m", "move", "o", "note", "d", "delete", "esc", "back")
}

// twoCol lays left and right on one line of w cells with right flush right.
func twoCol(left, right string, w int) string {
	pad := w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 2 {
		return left
	}
	return left + strings.Repeat(" ", pad) + right
}

// footerIntel renders the footer with the session intelligence slot on the
// right of line 1: today's tokens, the five-hour limit at 23% with its reset
// marker, and the f12 hint that rides the last third of the slot's turn.
func footerIntel() string {
	f := &components.Footer{
		Session:      "turn 14",
		Tokens:       12480,
		ContextLimit: 200000,
		Model:        "claude-sonnet-4-5",
		Effort:       "high",
		Provider:     "anthropic",
		Mode:         "agent",
		Agent:        "default",
		Width:        width,
		Cwd:          "/home/chris/GitHub/belai",
		Branch:       "main",
		Guardrails:   true,
		Ask:          true,
		SessionName:  "seal-demo",
		ShowName:     true,
		Intel: &components.IntelGauge{
			Today: "34.4M", Limit: "5h 23%", LimitFrac: 0.23, ElapsedFrac: 0.55,
			Spark: make([]int64, 24), Pace: "comfortable", State: components.BudgetTeal,
		},
	}
	return f.View()
}

// intel renders the f12 pane: the runs panel's intel tab with a five-hour and a
// weekly limit, pace, trend, runway, and today, the week and 30 days. The
// numbers are fixed so the capture is byte-stable.
func intel() string {
	teal := lipgloss.NewStyle().Foreground(components.ColorTeal)
	muted, low, emph := components.MutedStyle, components.LowStyle, components.EmphStyle
	tabs := muted.Render("activity  subagents  processes  kanban  crew  git  ") + emph.Render("[ intel ]")
	head := twoCol(tabs, muted.Render("tab switch · f12 close"), width)

	limit := func(title string, used, elapsed float64, resets string) string {
		left := "  " + muted.Render(fmt.Sprintf("%-18s", title)) +
			components.LimitBar(used, elapsed, 30, components.ColorTeal) + " " +
			teal.Render(fmt.Sprintf("%3d%%", int(used*100+0.5)))
		return twoCol(left, muted.Render("resets in "+resets), width)
	}
	spark := make([]int64, 24)
	for i, v := range []int64{0, 0, 0, 0, 0, 0, 1, 3, 5, 9, 6, 4, 2, 0, 0, 3, 8, 12, 7, 4, 2, 1, 0, 0} {
		spark[i] = v
	}
	row := func(label, pic, tokens, sessions string) string {
		return "  " + muted.Render(fmt.Sprintf("%-14s", label)) + pic + "  " + muted.Render(fmt.Sprintf("%7s  %s", tokens, sessions))
	}
	lines := []string{
		head,
		twoCol("  "+muted.Render("anthropic · ")+emph.Render("claude-sonnet-4-5"),
			muted.Render("today ")+emph.Render("34.4M")+muted.Render(" · 90 calls · 3 sessions")+low.Render("    live 8s ago"), width),
		limit("5-hour limit", 0.23, 0.55, "3h 40m"),
		limit("weekly limit", 0.48, 0.42, "4d 2h"),
		twoCol("  "+muted.Render("pace     ")+teal.Render("comfortable")+muted.Render(" · 1.5%/h · 410k tok/h"),
			muted.Render("trend  ")+teal.Render("↘ easing")+muted.Render(" · 0.5× the 7-day average"), width),
		"  " + muted.Render("runway   ") + teal.Render("lasts past the reset"),
		row("today", components.Sparkline(spark, 24, teal, low)+low.Render(" 24h"), "34.4M", "3 sessions"),
		row("this week", components.LimitBar(0.52, -1, 24, components.ColorTeal), "625.3M", "19 sessions"),
		row("last 30 days", components.LimitBar(1, -1, 24, components.ColorTeal), "1.2B", "22 sessions"),
		components.HelpBar("↑↓", "select", "←→", "window", "m", "models", "r", "roles", "t", "timeline", "b", "budgets", "⏎", "full screen", "esc", "unfocus"),
		components.Rule(width),
	}
	return strings.Join(lines, "\n")
}
