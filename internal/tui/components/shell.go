package components

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ShellRole is the transcript role of a `!cmd` panel. It is render-only: the
// row holds the command's raw output for the user, and no model ever sees it.
// The model gets the sanitised, classified copy as a shell attachment on the
// user turn instead.
const ShellRole = "shell"

// ShellTitle prefixes every shell panel's title.
const ShellTitle = "shell"

// shellPreviewLines is how much of a finished command a collapsed panel shows.
// The panel is tail-anchored, like a Bash tool row, because a command's
// verdict is in its last lines.
const shellPreviewLines = 6

// ShellArgs is the ToolArgs a shell row carries: the command, as JSON, so the
// row round-trips through the session file the same way a tool row does.
func ShellArgs(command string) string {
	b, _ := json.Marshal(map[string]string{"command": command})
	return string(b)
}

// ShellCommand returns the command a shell row ran, or "".
func (m Message) ShellCommand() string {
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(m.ToolArgs), &args) != nil {
		return ""
	}
	return args.Command
}

// shellPanel renders a `!cmd` as its own framed panel: the command in the
// title, the status and copy hint on the right, and the raw output beneath.
// Collapsed, it shows the last shellPreviewLines lines under a hint whose
// selection copies the rest; ctrl+o shows everything.
//
// The output is the command's own bytes, so it is laid out without its escape
// sequences and each row goes through NewSeg, which strips anything else that
// could steer the terminal. The row's Text keeps the bytes as they were.
func shellPanel(msg Message, width int, expandAll bool) (string, LineMap) {
	inner := max(width-4, 8)
	expand := expandAll || msg.Expanded
	content := strings.TrimRight(ansi.Strip(msg.Text()), "\n")
	running := !msg.StartedAt.IsZero() && msg.Text() == ""

	status := strings.TrimSpace(msg.Status)
	if running {
		status = "· " + time.Since(msg.StartedAt).Round(100*time.Millisecond).String()
	}
	meta := status
	if strings.TrimSpace(content) != "" {
		if meta != "" {
			meta += " · "
		}
		meta += "ctrl+c copy"
	}

	var rows []Row
	switch {
	case content == "" && msg.HasProgress():
		preview, trunc := shellProgressPreview(msg, expand)
		rows = contentRows(ansi.Strip(preview), "", inner, false, trunc)
	case content == "" && running:
		rows = []Row{{Segs: []Seg{NewSeg("running…", ColorMuted)}}}
	case content == "":
		rows = []Row{{Segs: []Seg{NewSeg("(no output)", ColorMuted)}}}
	default:
		preview, trunc := content, truncation{}
		if !expand {
			preview, trunc = tailPreview(content, shellPreviewLines)
		}
		isErr := strings.HasPrefix(status, "✗") || toolResultIsError("Bash", msg.Text())
		rows = contentRows(preview, "", inner, isErr, trunc)
	}

	title := ShellTitle
	if cmd := msg.ShellCommand(); cmd != "" {
		title += " · $ " + truncateRunes(strings.ReplaceAll(cmd, "\n", " "), 120)
	}
	return Panel{
		Title:    title,
		Meta:     meta,
		Width:    width,
		Accent:   lipgloss.TerminalColor(ColorAmber),
		BodyRows: rows,
	}.Render()
}

// shellProgressPreview is the live tail of a running command, sized to the
// panel's collapsed preview rather than a tool row's.
func shellProgressPreview(msg Message, expand bool) (string, truncation) {
	n := shellPreviewLines
	if expand {
		n = progressRingLines
	}
	lines, earlier := msg.ProgressTail(n)
	preview := strings.Join(lines, "\n")
	if earlier <= 0 {
		return preview, truncation{}
	}
	return preview, truncation{
		hidden: preview,
		label:  "… " + strconv.Itoa(earlier) + " earlier lines",
		atTop:  true,
	}
}
