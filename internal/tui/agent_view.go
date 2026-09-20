package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/tui/components"
)

// agentViewState tracks the /agent list/edit UI.
type agentViewState struct {
	profiles           []agentprofile.AgentProfile
	selected           int
	editMode           bool
	fieldEdit          bool // inline single-line / multiline editor open
	toolEdit           bool // tool multi-select sub-mode
	confirmDelete      bool // destructive delete confirmation in the editor
	fields             []agentField
	fieldSel           int
	fieldScroll        int // window offset for the field grid
	toolSel            int
	toolMarks          map[string]bool
	origFile           string // file name the profile was loaded from
	pendingEditProfile string
	errorMsg           string
}

// agentField is one editable agent-profile property.
type agentField struct {
	key     string
	label   string
	section string
	kind    string // text | choose | toggle | tools | multiline
	opts    []string
	value   string
	set     func(*agentprofile.AgentProfile, string) error
}

// agentFieldEditedMsg carries the result of an external $EDITOR run on a
// multiline agent field.
type agentFieldEditedMsg struct {
	fieldKey string
	path     string
	err      error
}

// loadAgentProfiles refreshes the list of stored agent profiles and clamps
// the selection. It is safe to call repeatedly because it is read-only and
// reports, rather than fails, disk errors.
func (a *App) loadAgentProfiles() {
	list, err := agentprofile.List()
	if err != nil {
		a.agentState.errorMsg = err.Error()
		a.agentState.profiles = nil
		return
	}
	a.agentState.profiles = list
	if a.agentState.selected < 0 || a.agentState.selected >= len(list) {
		a.agentState.selected = 0
	}
}

// enterAgentView is the view enter hook for /agent list. It reloads the list
// from disk and, when a create just finished, opens the new profile for
// editing.
func (a *App) enterAgentView() tea.Cmd {
	a.loadAgentProfiles()
	a.agentState.editMode = false
	a.agentState.fieldEdit = false
	a.agentState.toolEdit = false
	a.agentState.confirmDelete = false
	a.agentState.fieldSel = 0
	a.agentState.fieldScroll = 0
	a.agentState.toolSel = 0
	a.agentState.toolMarks = nil
	a.agentState.errorMsg = ""
	if a.agentState.pendingEditProfile != "" {
		for i, p := range a.agentState.profiles {
			if p.Name == a.agentState.pendingEditProfile {
				a.enterAgentEdit(i)
				break
			}
		}
		a.agentState.pendingEditProfile = ""
	}
	return nil
}

// enterAgentEdit owns the list→edit transition. Every edit entry point calls
// it, so "editMode implies non-empty fields" has a single owner.
func (a *App) enterAgentEdit(i int) {
	if i < 0 || i >= len(a.agentState.profiles) {
		return
	}
	p := &a.agentState.profiles[i]
	a.agentState.selected = i
	a.agentState.editMode = true
	a.agentState.fieldEdit = false
	a.agentState.toolEdit = false
	a.agentState.confirmDelete = false
	a.agentState.fields = a.buildAgentFields(*p)
	a.agentState.fieldSel = 0
	a.agentState.fieldScroll = 0
	a.agentState.toolSel = 0
	a.agentState.toolMarks = nil
	a.agentState.origFile = p.File
	a.agentState.errorMsg = ""
}

func (a *App) agentView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Background Agents", "esc back", w))
	if a.agentState.errorMsg != "" {
		b.WriteString(components.DangerStyle.Render("✗ "+a.agentState.errorMsg) + "\n\n")
	}
	if a.agentState.editMode {
		b.WriteString(a.agentEditView(w))
	} else {
		b.WriteString(a.agentListView(w))
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) agentListView(w int) string {
	var b strings.Builder
	list := a.agentState.profiles
	running := map[string]bgagent.AgentStatus{}
	if a.bgManager != nil {
		for _, s := range a.bgManager.List() {
			running[s.Name] = s
		}
	}
	if len(list) == 0 {
		b.WriteString(components.MutedStyle.Render("  no agents discovered — /agent create <name> to make one") + "\n")
	} else {
		for i, p := range list {
			selected := i == a.agentState.selected
			cursor := components.Cursor(selected)
			name := components.EmphStyle.Render(p.Name)
			if !selected {
				name = components.MutedStyle.Render(p.Name)
			}
			path := "embedded"
			if !p.Builtin {
				dir, _ := agentprofile.Dir()
				path = filepath.Join(dir, p.FileName())
			}
			modeChip := components.Chip(p.Mode, components.ColorAmber)
			autonomy := p.Autonomy
			if autonomy == "" {
				autonomy = agentprofile.AutonomySupervised
			}
			line := fmt.Sprintf("%s%s %s %s %s", cursor, name, modeChip, components.MutedStyle.Render(autonomy), components.MutedStyle.Render(path))
			b.WriteString(lipgloss.NewStyle().MaxWidth(w).Render(line) + "\n")
			if p.Description != "" {
				desc := "  " + p.Description
				if len(desc) > w-2 {
					desc = desc[:w-5] + "…"
				}
				b.WriteString(components.MutedStyle.Render(desc) + "\n")
			}
			if s, ok := running[p.Name]; ok {
				state := fmt.Sprintf("  running: %s iter=%d", s.State, s.Iteration)
				if s.LastOutput != "" {
					out := s.LastOutput
					if len(out) > w-6 {
						out = out[:w-6] + "…"
					}
					state += "\n    " + out
				}
				b.WriteString(components.WarnStyle.Render(state) + "\n")
			}
		}
	}
	b.WriteString("\n" + components.HelpBar("up, down", "move", "enter, e", "edit", "n", "new", "d", "duplicate", "esc", "back") + "\n")
	return b.String()
}

func (a *App) agentEditView(w int) string {
	p := a.selectedAgentProfile()
	if p == nil || len(a.agentState.fields) == 0 {
		a.agentState.editMode = false
		a.agentState.fieldEdit = false
		a.agentState.toolEdit = false
		return a.agentListView(w)
	}
	a.clampAgentFieldScroll()

	var b strings.Builder
	b.WriteString(components.AccentStyle.Render("Editing ") + components.EmphStyle.Render(p.Name))
	if p.Builtin {
		b.WriteString(" " + components.Chip("built-in", components.ColorAmber))
	}
	b.WriteString("\n")
	if p.Builtin {
		b.WriteString(components.MutedStyle.Render("embedded built-in — not stored on disk") + "\n\n")
	} else {
		dir, _ := agentprofile.Dir()
		b.WriteString(components.MutedStyle.Render(filepath.Join(dir, p.FileName())) + "\n\n")
	}

	if a.agentState.fieldEdit {
		row := a.agentState.fields[a.agentState.fieldSel]
		if row.kind == "multiline" {
			b.WriteString(a.renderAgentMultilineEditor(row.label, w) + "\n")
			b.WriteString("\n" + components.HelpBar("enter", "save", "ctrl+j", "newline", "e", "$EDITOR", "esc", "cancel") + "\n")
		} else {
			b.WriteString(a.renderFieldEditor(row.label, w) + "\n")
			b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
		}
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	if a.agentState.toolEdit {
		b.WriteString(a.renderToolPicker(w))
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "space", "toggle", "a", "all", "n", "none", "enter", "commit", "esc", "cancel") + "\n")
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}

	visible := a.agentEditWindowHeight()
	if visible < 1 {
		visible = 1
	}
	end := a.agentState.fieldScroll + visible
	if end > len(a.agentState.fields) {
		end = len(a.agentState.fields)
	}
	if a.agentState.fieldScroll > 0 {
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("↑%d more", a.agentState.fieldScroll)) + "\n")
	}
	valueWidth := w - 24
	if valueWidth < 4 {
		valueWidth = 4
	}
	for i := a.agentState.fieldScroll; i < end; i++ {
		f := a.agentState.fields[i]
		if i == a.agentState.fieldScroll || (i > 0 && f.section != a.agentState.fields[i-1].section) {
			b.WriteString(components.MutedStyle.Render("── "+f.section+" ──") + "\n")
		}
		selected := i == a.agentState.fieldSel
		label := fmt.Sprintf("%-18s", f.label)
		rawValue := a.agentFieldDisplayValue(f)
		rawValue = truncateValue(rawValue, valueWidth)
		var value string
		switch {
		case f.kind == "tools" && len(p.Tools) == 0:
			value = components.MutedStyle.Render(rawValue)
		case selected:
			value = components.EmphStyle.Render(rawValue)
		default:
			value = rawValue
		}
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
		} else {
			label = components.MutedStyle.Render(label)
		}
		marker := ""
		if f.key == "schedule" && p.Mode == agentprofile.ModeScheduled && p.Schedule == "" {
			marker = components.MutedStyle.Render(" required")
		}
		if f.key == "monitor_condition" && p.Mode == agentprofile.ModeMonitor && p.MonitorCondition == "" {
			marker = components.MutedStyle.Render(" required")
		}
		if f.key == "mode" && agentModeIsDerived(*p) {
			marker = components.MutedStyle.Render(" auto")
		}
		b.WriteString(components.Cursor(selected) + label + "  " + value + marker + "\n")
	}
	if end < len(a.agentState.fields) {
		b.WriteString(components.MutedStyle.Render(fmt.Sprintf("↓%d more", len(a.agentState.fields)-end)) + "\n")
	}
	if a.agentState.confirmDelete {
		b.WriteString("\n" + components.DangerStyle.Render(fmt.Sprintf("Delete profile %q?", p.Name)) + "\n")
		b.WriteString(components.HelpBar("y", "delete", "n/esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar("↑↓", "move", "←/→", "cycle", "space/enter", "edit", "n", "new", "d", "duplicate", "x", "delete", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) renderAgentMultilineEditor(title string, width int) string {
	return components.Panel{
		Title:  title,
		Meta:   "⏎ save · ctrl+j newline · esc cancel",
		Body:   a.editor.View(),
		Width:  width,
		Accent: lipgloss.TerminalColor(components.ColorTeal),
		Raw:    true,
	}.View()
}

func (a *App) renderToolPicker(w int) string {
	var b strings.Builder
	b.WriteString(components.AccentStyle.Render("Tools") + "\n")
	opts := agentprofile.KnownTools()
	for i, t := range opts {
		cursor := components.Cursor(i == a.agentState.toolSel)
		mark := "○ "
		if a.agentState.toolMarks[t] {
			mark = "● "
		}
		line := cursor + mark + t
		if t == "Write" || t == "Edit" || t == "Bash" {
			line += components.MutedStyle.Render(" mutates")
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (a *App) selectedAgentProfile() *agentprofile.AgentProfile {
	if a.agentState.selected >= 0 && a.agentState.selected < len(a.agentState.profiles) {
		return &a.agentState.profiles[a.agentState.selected]
	}
	return nil
}

func (a *App) selectedAgentIsBuiltin() bool {
	p := a.selectedAgentProfile()
	return p != nil && (p.Builtin || agentprofile.IsBuiltin(p.Name))
}

func (a *App) buildAgentFields(p agentprofile.AgentProfile) []agentField {
	autonomy := p.Autonomy
	if autonomy == "" {
		autonomy = agentprofile.AutonomySupervised
	}
	max := ""
	if p.MaxIterations > 0 {
		max = strconv.Itoa(p.MaxIterations)
	}
	fileValue := p.File
	if fileValue == "" {
		fileValue = p.FileName()
	}
	sysValue := systemPromptSummary(p.SystemPrompt)
	toolsValue := "all (inherit)"
	if len(p.Tools) > 0 {
		toolsValue = fmt.Sprintf("%s (%d/%d)", strings.Join(p.Tools, " · "), len(p.Tools), len(agentprofile.KnownTools()))
	}
	// Mode is derived from schedule/monitor_condition when either is set:
	// schedule makes the profile scheduled, monitor_condition makes it monitor,
	// otherwise the user toggles between single and loop.
	modeValue := agentDerivedMode(p)
	return []agentField{
		{key: "name", label: "name", section: "identity", kind: "text", value: p.Name, set: func(dst *agentprofile.AgentProfile, v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				return fmt.Errorf("name is required")
			}
			if dst.File == "" || dst.File == deriveProfileFileName(dst.Name) {
				dst.File = "" // keep the file name derived from Name
			}
			dst.Name = v
			return nil
		}},
		{key: "file_name", label: "file name", section: "identity", kind: "text", value: fileValue, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.File = strings.TrimSpace(v)
			return nil
		}},
		{key: "description", label: "description", section: "identity", kind: "text", value: p.Description, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Description = strings.TrimSpace(v)
			return nil
		}},
		{key: "system_prompt", label: "system prompt", section: "behaviour", kind: "multiline", value: sysValue, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.SystemPrompt = strings.TrimSpace(v)
			return nil
		}},
		{key: "tools", label: "tools", section: "behaviour", kind: "tools", value: toolsValue, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Tools = nil
			for _, t := range strings.Split(v, "\n") {
				t = strings.TrimSpace(t)
				if t != "" {
					dst.Tools = append(dst.Tools, t)
				}
			}
			return nil
		}},
		{key: "mode", label: "mode", section: "behaviour", kind: "choose", opts: []string{agentprofile.ModeSingle, agentprofile.ModeLoop}, value: modeValue, set: func(dst *agentprofile.AgentProfile, v string) error {
			if dst.Schedule != "" {
				return fmt.Errorf("clear schedule before changing mode")
			}
			if dst.MonitorCondition != "" {
				return fmt.Errorf("clear monitor_condition before changing mode")
			}
			switch v {
			case agentprofile.ModeSingle, agentprofile.ModeLoop:
				dst.Mode = v
			default:
				return fmt.Errorf("invalid mode %q", v)
			}
			return nil
		}},
		{key: "schedule", label: "schedule", section: "behaviour", kind: "text", value: p.Schedule, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Schedule = strings.TrimSpace(v)
			if dst.Schedule != "" {
				dst.Mode = agentprofile.ModeScheduled
			} else if dst.Mode == agentprofile.ModeScheduled {
				dst.Mode = agentprofile.ModeSingle
			}
			return nil
		}},
		{key: "monitor_condition", label: "monitor condition", section: "behaviour", kind: "text", value: p.MonitorCondition, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.MonitorCondition = strings.TrimSpace(v)
			if dst.MonitorCondition != "" {
				dst.Mode = agentprofile.ModeMonitor
			} else if dst.Mode == agentprofile.ModeMonitor {
				dst.Mode = agentprofile.ModeSingle
			}
			return nil
		}},
		{key: "provider", label: "provider", section: "model", kind: "choose", opts: append([]string{""}, a.providerNames()...), value: p.Provider, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Provider = v
			return nil
		}},
		{key: "model", label: "model", section: "model", kind: "choose", opts: a.agentModelOpts(p.Provider), value: p.Model, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Model = v
			return nil
		}},
		{key: "effort", label: "effort", section: "model", kind: "choose", opts: append([]string{""}, agentprofile.ValidEfforts()...), value: p.Effort, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Effort = v
			return nil
		}},
		{key: "autonomy", label: "autonomy", section: "safety", kind: "choose", opts: []string{agentprofile.AutonomySupervised, agentprofile.AutonomyAutonomous}, value: autonomy, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Autonomy = v
			return nil
		}},
		{key: "guardrails", label: "guardrails", section: "safety", kind: "choose", opts: []string{"inherit", "on", "off"}, value: boolPtrLabel(p.Guardrails), set: func(dst *agentprofile.AgentProfile, v string) error {
			return setBoolPtrFromChoice(&dst.Guardrails, v)
		}},
		{key: "ask_permission", label: "ask permission", section: "safety", kind: "choose", opts: []string{"inherit", "on", "off"}, value: boolPtrLabel(p.AskPermission), set: func(dst *agentprofile.AgentProfile, v string) error {
			return setBoolPtrFromChoice(&dst.AskPermission, v)
		}},
		{key: "reflection", label: "reflection", section: "safety", kind: "toggle", value: boolLabel(p.Reflection), set: func(dst *agentprofile.AgentProfile, v string) error {
			switch v {
			case "on":
				dst.Reflection = true
			case "off":
				dst.Reflection = false
			default:
				return fmt.Errorf("reflection must be on or off")
			}
			return nil
		}},
		{key: "max_iterations", label: "max iterations", section: "safety", kind: "text", value: max, set: func(dst *agentprofile.AgentProfile, v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				dst.MaxIterations = 0
				return nil
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("max_iterations must be a non-negative integer")
			}
			dst.MaxIterations = n
			return nil
		}},
	}
}

func (a *App) handleAgentKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.agentState.editMode && len(a.agentState.fields) == 0 {
		a.agentState.editMode = false
		a.agentState.fieldEdit = false
		a.agentState.toolEdit = false
	}

	if a.agentState.toolEdit {
		return a.handleAgentToolKey(m)
	}
	if a.agentState.editMode && a.agentState.fieldEdit {
		return a.handleAgentFieldEditKey(m)
	}
	if a.agentState.editMode {
		return a.handleAgentEditKey(m)
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.agentState.selected > 0 {
			a.agentState.selected--
		}
		return a, nil
	case "down", "j":
		if a.agentState.selected < len(a.agentState.profiles)-1 {
			a.agentState.selected++
		}
		return a, nil
	case "enter", "e":
		if a.agentState.selected >= 0 && a.agentState.selected < len(a.agentState.profiles) {
			a.enterAgentEdit(a.agentState.selected)
		}
		return a, nil
	case "n":
		a.newAgent()
		return a, nil
	case "d":
		a.duplicateAgent()
		return a, nil
	}
	return a, nil
}

func (a *App) handleAgentEditKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.agentState.confirmDelete {
		switch m.String() {
		case "y":
			if err := a.deleteAgent(); err != nil {
				a.agentState.errorMsg = err.Error()
				a.agentState.confirmDelete = false
			}
			return a, nil
		case "n", "esc":
			a.agentState.confirmDelete = false
			a.agentState.errorMsg = ""
			return a, nil
		}
		return a, nil
	}

	switch m.String() {
	case "esc":
		a.agentState.editMode = false
		a.agentState.fieldEdit = false
		a.agentState.toolEdit = false
		a.agentState.confirmDelete = false
		a.agentState.fieldSel = 0
		a.agentState.fieldScroll = 0
		a.agentState.errorMsg = ""
		return a, nil
	case "up", "k":
		if a.agentState.fieldSel > 0 {
			a.agentState.fieldSel--
			a.clampAgentFieldScroll()
		}
		return a, nil
	case "down", "j":
		if a.agentState.fieldSel < len(a.agentState.fields)-1 {
			a.agentState.fieldSel++
			a.clampAgentFieldScroll()
		}
		return a, nil
	case "n":
		a.newAgent()
		return a, nil
	case "d":
		a.duplicateAgent()
		return a, nil
	case "x":
		if a.selectedAgentIsBuiltin() {
			a.agentState.errorMsg = "built-in profile is read-only — d duplicates it"
			return a, nil
		}
		a.agentState.confirmDelete = true
		a.agentState.errorMsg = ""
		return a, nil
	case "left", "right":
		if a.selectedAgentIsBuiltin() {
			a.agentState.errorMsg = "built-in profile is read-only — d duplicates it"
			return a, nil
		}
		row := &a.agentState.fields[a.agentState.fieldSel]
		if row.key == "mode" {
			if p := a.selectedAgentProfile(); p != nil && agentModeIsDerived(*p) {
				a.agentState.errorMsg = "mode is set by schedule/monitor condition"
				return a, nil
			}
		}
		var next string
		switch row.kind {
		case "choose":
			if m.String() == "left" {
				next = prevChoice(row.opts, row.value)
			} else {
				next = nextChoice(row.opts, row.value)
			}
		case "toggle":
			if row.value == "on" {
				next = "off"
			} else {
				next = "on"
			}
		default:
			return a, nil
		}
		if err := a.applyAgentFieldChange(*row, next); err != nil {
			a.agentState.errorMsg = err.Error()
		} else {
			a.agentState.errorMsg = ""
		}
		return a, nil
	case " ", "enter":
		if a.selectedAgentIsBuiltin() {
			a.agentState.errorMsg = "built-in profile is read-only — d duplicates it"
			return a, nil
		}
		row := &a.agentState.fields[a.agentState.fieldSel]
		switch row.kind {
		case "toggle":
			next := "on"
			if row.value == "on" {
				next = "off"
			}
			if err := a.applyAgentFieldChange(*row, next); err != nil {
				a.agentState.errorMsg = err.Error()
			} else {
				a.agentState.errorMsg = ""
			}
		case "choose":
			if row.key == "mode" {
				if p := a.selectedAgentProfile(); p != nil && agentModeIsDerived(*p) {
					a.agentState.errorMsg = "mode is set by schedule/monitor condition"
					return a, nil
				}
			}
			next := nextChoice(row.opts, row.value)
			if err := a.applyAgentFieldChange(*row, next); err != nil {
				a.agentState.errorMsg = err.Error()
			} else {
				a.agentState.errorMsg = ""
			}
		case "text", "multiline":
			a.agentState.fieldEdit = true
			a.agentState.errorMsg = ""
			if row.kind == "multiline" {
				if p := a.selectedAgentProfile(); p != nil {
					a.editor.SetValue(p.SystemPrompt)
				} else {
					a.editor.SetValue(row.value)
				}
			} else {
				a.editor.SetValue(row.value)
			}
			a.editor.CursorEnd()
			_ = a.editor.Focus()
		case "tools":
			a.agentState.toolEdit = true
			a.agentState.toolSel = 0
			a.agentState.toolMarks = map[string]bool{}
			if p := a.selectedAgentProfile(); p != nil {
				for _, t := range p.Tools {
					a.agentState.toolMarks[t] = true
				}
			}
			a.agentState.errorMsg = ""
		}
		return a, nil
	}
	return a, nil
}

func (a *App) handleAgentFieldEditKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	row := a.agentState.fields[a.agentState.fieldSel]
	switch m.String() {
	case "esc":
		a.agentState.fieldEdit = false
		a.agentState.errorMsg = ""
		a.editor.Reset()
		return a, nil
	case "enter":
		err := a.applyAgentFieldChange(row, a.editor.Value())
		a.editor.Reset()
		if err != nil {
			a.agentState.errorMsg = err.Error()
			return a, nil
		}
		a.agentState.fieldEdit = false
		a.agentState.errorMsg = ""
		return a, nil
	case "e":
		if row.kind == "multiline" {
			return a, a.openAgentFieldEditor(row)
		}
	}
	return a, a.editor.Update(m)
}

func (a *App) handleAgentToolKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	opts := agentprofile.KnownTools()
	switch m.String() {
	case "esc":
		a.agentState.toolEdit = false
		a.agentState.toolMarks = nil
		a.agentState.errorMsg = ""
		return a, nil
	case "up", "k":
		if a.agentState.toolSel > 0 {
			a.agentState.toolSel--
		}
		return a, nil
	case "down", "j":
		if a.agentState.toolSel < len(opts)-1 {
			a.agentState.toolSel++
		}
		return a, nil
	case " ":
		if a.agentState.toolSel >= 0 && a.agentState.toolSel < len(opts) {
			t := opts[a.agentState.toolSel]
			if a.agentState.toolMarks[t] {
				delete(a.agentState.toolMarks, t)
			} else {
				a.agentState.toolMarks[t] = true
			}
		}
		return a, nil
	case "a":
		for _, t := range opts {
			a.agentState.toolMarks[t] = true
		}
		return a, nil
	case "n":
		a.agentState.toolMarks = map[string]bool{}
		return a, nil
	case "enter":
		if a.selectedAgentIsBuiltin() {
			a.agentState.errorMsg = "built-in profile is read-only — d duplicates it"
			return a, nil
		}
		selected := make([]string, 0, len(a.agentState.toolMarks))
		for t := range a.agentState.toolMarks {
			if a.agentState.toolMarks[t] {
				selected = append(selected, t)
			}
		}
		sort.Strings(selected)
		row := a.agentState.fields[a.agentState.fieldSel]
		if err := a.applyAgentFieldChange(row, strings.Join(selected, "\n")); err != nil {
			a.agentState.errorMsg = err.Error()
			return a, nil
		}
		a.agentState.toolEdit = false
		a.agentState.toolMarks = nil
		a.agentState.errorMsg = ""
		return a, nil
	}
	return a, nil
}

// openAgentFieldEditor hands a multiline field to $VISUAL/$EDITOR, falling
// back to the in-TUI editor when neither resolves. It runs through the
// execEditor seam, mirroring openPromptEditor.
func (a *App) openAgentFieldEditor(row agentField) tea.Cmd {
	p := a.selectedAgentProfile()
	if p == nil {
		return nil
	}
	f, err := os.CreateTemp("", "signet-agent-field-*.md")
	if err != nil {
		a.addSystem("editor failed: " + err.Error())
		return nil
	}
	path := f.Name()
	if _, err := f.WriteString(p.SystemPrompt); err != nil {
		f.Close()
		os.Remove(path)
		a.addSystem("editor failed: " + err.Error())
		return nil
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		a.addSystem("editor failed: " + err.Error())
		return nil
	}

	bin, args, ok := resolveEditorCommand()
	if !ok {
		os.Remove(path)
		a.addSystem("no $VISUAL or $EDITOR found; edit inline")
		a.agentState.fieldEdit = true
		a.editor.SetValue(p.SystemPrompt)
		a.editor.CursorEnd()
		_ = a.editor.Focus()
		return nil
	}

	a.agentState.fieldEdit = false
	a.editor.Reset()
	cmd := exec.Command(bin, append(args, path)...)
	return a.execEditor(cmd, func(err error) tea.Msg {
		return agentFieldEditedMsg{fieldKey: row.key, path: path, err: err}
	})
}

// handleAgentFieldEdited reads the externally edited field back and applies it
// through the normal save-per-field path. It also restores the mouse, which
// tea's terminal release disabled.
func (a *App) handleAgentFieldEdited(m agentFieldEditedMsg) tea.Cmd {
	cmds := []tea.Cmd{}
	if mouseEnabled(&a.settings) {
		cmds = append(cmds, func() tea.Msg { return tea.EnableMouseCellMotion() })
	}
	if m.err != nil {
		a.addSystem("editor failed: " + m.err.Error())
		return tea.Batch(cmds...)
	}
	data, err := os.ReadFile(m.path)
	_ = os.Remove(m.path)
	if err != nil {
		a.addSystem("editor failed: " + err.Error())
		return tea.Batch(cmds...)
	}
	for i := range a.agentState.fields {
		if a.agentState.fields[i].key == m.fieldKey {
			if err := a.applyAgentFieldChange(a.agentState.fields[i], string(data)); err != nil {
				a.agentState.errorMsg = err.Error()
			} else {
				a.agentState.errorMsg = ""
			}
			a.agentState.fieldEdit = false
			return tea.Batch(cmds...)
		}
	}
	return tea.Batch(cmds...)
}

// applyAgentFieldChange writes one field change to a copy of the selected
// profile, validates it, persists it, and reloads the in-memory list. If
// validation or saving fails, the on-disk profile is untouched and the
// error is returned.
func (a *App) applyAgentFieldChange(row agentField, raw string) error {
	p := a.selectedAgentProfile()
	if p == nil {
		return fmt.Errorf("no agent selected")
	}
	if p.Builtin || agentprofile.IsBuiltin(p.Name) {
		return fmt.Errorf("built-in profile is read-only")
	}
	changed := *p
	if err := row.set(&changed, raw); err != nil {
		return err
	}
	path, err := agentprofile.SaveMoving(changed, a.agentState.origFile)
	if path != "" {
		a.agentState.origFile = changed.FileName()
	}
	a.loadAgentProfiles()
	found := false
	for i, prof := range a.agentState.profiles {
		if prof.Name == changed.Name {
			a.agentState.selected = i
			found = true
			break
		}
	}
	if !found {
		// The re-find can fail when a name edit collided or was refused; never
		// index a profile list that no longer holds the selection.
		if a.agentState.selected >= len(a.agentState.profiles) {
			a.agentState.selected = 0
		}
	}
	if len(a.agentState.profiles) > 0 {
		a.agentState.fields = a.buildAgentFields(a.agentState.profiles[a.agentState.selected])
	} else {
		a.agentState.fields = nil
	}
	return err
}

func nextChoice(opts []string, current string) string {
	if len(opts) == 0 {
		return current
	}
	idx := -1
	for i, o := range opts {
		if o == current {
			idx = i
			break
		}
	}
	return opts[(idx+1)%len(opts)]
}

func prevChoice(opts []string, current string) string {
	if len(opts) == 0 {
		return current
	}
	idx := -1
	for i, o := range opts {
		if o == current {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
	}
	return opts[(idx-1+len(opts))%len(opts)]
}

// agentModelOpts returns the selectable model IDs for an agent profile's
// chosen provider, plus an empty "inherit" stop.
func (a *App) agentModelOpts(provider string) []string {
	opts := []string{""}
	for _, m := range a.catalogFor(provider) {
		opts = append(opts, m.ID)
	}
	return opts
}

// agentDerivedMode returns the effective display mode for a profile. When a
// schedule or monitor condition is present, mode is forced to the matching
// derived value regardless of the stored Mode field, so the UI always shows
// the profile as scheduled/monitor while those fields are set.
func agentDerivedMode(p agentprofile.AgentProfile) string {
	if p.Schedule != "" {
		return agentprofile.ModeScheduled
	}
	if p.MonitorCondition != "" {
		return agentprofile.ModeMonitor
	}
	return p.Mode
}

// agentModeIsDerived reports whether the mode is currently controlled by the
// schedule or monitor_condition field rather than by the mode toggle.
func agentModeIsDerived(p agentprofile.AgentProfile) bool {
	return p.Schedule != "" || p.MonitorCondition != ""
}

func (a *App) agentEditWindowHeight() int {
	h := a.height - 10
	if h < 1 {
		h = 1
	}
	return h
}

func (a *App) clampAgentFieldScroll() {
	n := len(a.agentState.fields)
	if n == 0 {
		a.agentState.fieldScroll = 0
		return
	}
	if a.agentState.fieldSel < 0 {
		a.agentState.fieldSel = 0
	}
	if a.agentState.fieldSel >= n {
		a.agentState.fieldSel = n - 1
	}
	h := a.agentEditWindowHeight()
	if h < 1 {
		h = 1
	}
	if h > n {
		h = n
	}
	if a.agentState.fieldSel < a.agentState.fieldScroll {
		a.agentState.fieldScroll = a.agentState.fieldSel
	}
	if a.agentState.fieldSel >= a.agentState.fieldScroll+h {
		a.agentState.fieldScroll = a.agentState.fieldSel - h + 1
	}
	if a.agentState.fieldScroll < 0 {
		a.agentState.fieldScroll = 0
	}
	if a.agentState.fieldScroll+h > n {
		a.agentState.fieldScroll = n - h
	}
	if a.agentState.fieldScroll < 0 {
		a.agentState.fieldScroll = 0
	}
}

func (a *App) agentFieldDisplayValue(f agentField) string {
	switch f.key {
	case "provider", "model", "effort":
		if f.value == "" {
			return "(inherit)"
		}
	}
	return f.value
}

func (a *App) newAgent() {
	name := a.uniqueAgentName("agent")
	stub := agentprofile.Stub(name)
	if _, err := agentprofile.Save(stub); err != nil {
		a.agentState.errorMsg = err.Error()
		return
	}
	a.reloadAgentProfilesAndSelect(name)
}

func (a *App) duplicateAgent() {
	p := a.selectedAgentProfile()
	if p == nil {
		return
	}
	name := strings.TrimPrefix(p.Name, agentprofile.BuiltinPrefix)
	if !p.Builtin {
		name = p.Name + "-copy"
	}
	if _, err := agentprofile.Load(name); err == nil {
		a.agentState.errorMsg = fmt.Sprintf("cannot duplicate: %s already exists", name)
		return
	}
	copy := *p
	copy.Builtin = false
	copy.Name = name
	copy.File = ""
	if _, err := agentprofile.Save(copy); err != nil {
		a.agentState.errorMsg = err.Error()
		return
	}
	a.reloadAgentProfilesAndSelect(name)
}

func (a *App) reloadAgentProfilesAndSelect(name string) {
	a.loadAgentProfiles()
	for i, p := range a.agentState.profiles {
		if p.Name == name {
			a.enterAgentEdit(i)
			return
		}
	}
	a.agentState.errorMsg = "profile saved but could not be selected"
}

// deleteAgent removes the selected non-built-in profile from disk, clears the
// editor, and reloads the profile list. Built-ins are rejected.
func (a *App) deleteAgent() error {
	p := a.selectedAgentProfile()
	if p == nil {
		return fmt.Errorf("no agent selected")
	}
	if p.Builtin || agentprofile.IsBuiltin(p.Name) {
		return fmt.Errorf("built-in profile cannot be deleted")
	}
	if err := agentprofile.Delete(p.Name); err != nil {
		return err
	}
	a.agentState.editMode = false
	a.agentState.fieldEdit = false
	a.agentState.toolEdit = false
	a.agentState.confirmDelete = false
	a.loadAgentProfiles()
	a.agentState.selected = 0
	return nil
}

func (a *App) uniqueAgentName(base string) string {
	name := base
	for i := 1; i < 100; i++ {
		if _, err := agentprofile.Load(name); err != nil {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return base
}

func systemPromptSummary(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= 1 {
		return s
	}
	return lines[0] + fmt.Sprintf(" +%d lines", len(lines)-1)
}

func deriveProfileFileName(name string) string {
	return (agentprofile.AgentProfile{Name: name}).FileName()
}

func truncateValue(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return ansi.Cut(s, 0, width-1) + "…"
}

// boolPtrLabel renders a *bool as a three-state label.
func boolPtrLabel(p *bool) string {
	if p == nil {
		return "inherit"
	}
	if *p {
		return "on"
	}
	return "off"
}

// setBoolPtrFromChoice updates a *bool from the three-state UI labels.
func setBoolPtrFromChoice(dst **bool, v string) error {
	switch v {
	case "inherit":
		*dst = nil
	case "on":
		b := true
		*dst = &b
	case "off":
		b := false
		*dst = &b
	default:
		return fmt.Errorf("choice must be inherit, on, or off")
	}
	return nil
}
