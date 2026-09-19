package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/tui/components"
)

// agentViewState tracks the /agent list/edit UI.
type agentViewState struct {
	profiles           []agentprofile.AgentProfile
	selected           int
	editMode           bool
	fieldEdit          bool
	fields             []agentField
	fieldSel           int
	pendingEditProfile string
	errorMsg           string
}

// agentField is one editable agent-profile property.
type agentField struct {
	key   string
	label string
	kind  string // text | choose | toggle
	opts  []string
	value string
	set   func(*agentprofile.AgentProfile, string) error
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
	if a.agentState.selected >= len(list) {
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
	a.agentState.fieldSel = 0
	a.agentState.errorMsg = ""
	if a.agentState.pendingEditProfile != "" {
		for i, p := range a.agentState.profiles {
			if p.Name == a.agentState.pendingEditProfile {
				a.agentState.selected = i
				a.agentState.editMode = true
				break
			}
		}
		a.agentState.pendingEditProfile = ""
	}
	return nil
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
		b.WriteString(components.MutedStyle.Render("  no agents discovered — /agent create <description> to make one") + "\n")
	} else {
		for i, p := range list {
			selected := i == a.agentState.selected
			cursor := components.Cursor(selected)
			name := components.EmphStyle.Render(p.Name)
			if !selected {
				name = components.MutedStyle.Render(p.Name)
			}
			dir, _ := agentprofile.Dir()
			path := filepath.Join(dir, p.FileName())
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
	b.WriteString("\n" + components.HelpBar("up, down", "move", "enter, e", "edit", "esc", "back") + "\n")
	return b.String()
}

func (a *App) agentEditView(w int) string {
	p := a.selectedAgentProfile()
	if p == nil {
		a.agentState.editMode = false
		return a.agentListView(w)
	}
	dir, _ := agentprofile.Dir()
	path := filepath.Join(dir, p.FileName())

	var b strings.Builder
	b.WriteString(components.AccentStyle.Render("Editing ") + components.EmphStyle.Render(p.Name) + "\n")
	b.WriteString(components.MutedStyle.Render(path) + "\n\n")

	if a.agentState.fieldEdit {
		b.WriteString(a.renderFieldEditor(a.agentState.fields[a.agentState.fieldSel].label, w) + "\n")
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
		return b.String()
	}

	for i, f := range a.agentState.fields {
		selected := i == a.agentState.fieldSel
		label := fmt.Sprintf("%-18s", f.label)
		value := f.value
		if selected {
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		} else {
			label = components.MutedStyle.Render(label)
		}
		b.WriteString(components.Cursor(selected) + label + "  " + value + "\n")
	}
	b.WriteString("\n" + components.HelpBar("↑↓", "move", "space/enter", "edit", "esc", "back") + "\n")
	return b.String()
}

func (a *App) selectedAgentProfile() *agentprofile.AgentProfile {
	if a.agentState.selected < len(a.agentState.profiles) {
		return &a.agentState.profiles[a.agentState.selected]
	}
	return nil
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
	return []agentField{
		{key: "description", label: "description", kind: "text", value: p.Description, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Description = strings.TrimSpace(v)
			return nil
		}},
		{key: "mode", label: "mode", kind: "choose", opts: []string{agentprofile.ModeSingle, agentprofile.ModeLoop, agentprofile.ModeScheduled, agentprofile.ModeMonitor}, value: p.Mode, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Mode = v
			return nil
		}},
		{key: "schedule", label: "schedule", kind: "text", value: p.Schedule, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Schedule = strings.TrimSpace(v)
			return nil
		}},
		{key: "monitor_condition", label: "monitor condition", kind: "text", value: p.MonitorCondition, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.MonitorCondition = strings.TrimSpace(v)
			return nil
		}},
		{key: "autonomy", label: "autonomy", kind: "choose", opts: []string{agentprofile.AutonomySupervised, agentprofile.AutonomyAutonomous}, value: autonomy, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Autonomy = v
			return nil
		}},
		{key: "max_iterations", label: "max iterations", kind: "text", value: max, set: func(dst *agentprofile.AgentProfile, v string) error {
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
		{key: "reflection", label: "reflection", kind: "toggle", value: boolLabel(p.Reflection), set: func(dst *agentprofile.AgentProfile, v string) error {
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
		{key: "provider", label: "provider", kind: "choose", opts: append([]string{""}, a.providerNames()...), value: p.Provider, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Provider = v
			return nil
		}},
		{key: "model", label: "model", kind: "choose", opts: a.agentModelOpts(p.Provider), value: p.Model, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Model = v
			return nil
		}},
		{key: "effort", label: "effort", kind: "choose", opts: append([]string{""}, defaultClassifierEfforts...), value: p.Effort, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.Effort = v
			return nil
		}},
		{key: "guardrails", label: "guardrails", kind: "choose", opts: []string{"inherit", "on", "off"}, value: boolPtrLabel(p.Guardrails), set: func(dst *agentprofile.AgentProfile, v string) error {
			return setBoolPtrFromChoice(&dst.Guardrails, v)
		}},
		{key: "ask", label: "ask", kind: "choose", opts: []string{"inherit", "on", "off"}, value: boolPtrLabel(p.AskPermission), set: func(dst *agentprofile.AgentProfile, v string) error {
			return setBoolPtrFromChoice(&dst.AskPermission, v)
		}},
		{key: "system_prompt", label: "system prompt", kind: "text", value: p.SystemPrompt, set: func(dst *agentprofile.AgentProfile, v string) error {
			dst.SystemPrompt = strings.TrimSpace(v)
			return nil
		}},
	}
}

func (a *App) handleAgentKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.agentState.editMode && a.agentState.fieldEdit {
		switch m.String() {
		case "esc":
			a.agentState.fieldEdit = false
			a.agentState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			row := a.agentState.fields[a.agentState.fieldSel]
			err := a.applyAgentFieldChange(row, a.editor.Value())
			a.editor.Reset()
			if err != nil {
				a.agentState.errorMsg = err.Error()
				return a, nil
			}
			a.agentState.fieldEdit = false
			a.agentState.errorMsg = ""
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	if a.agentState.editMode {
		switch m.String() {
		case "esc":
			a.agentState.editMode = false
			a.agentState.fieldEdit = false
			a.agentState.fieldSel = 0
			a.agentState.errorMsg = ""
			return a, nil
		case "up", "k":
			if a.agentState.fieldSel > 0 {
				a.agentState.fieldSel--
			}
			return a, nil
		case "down", "j":
			if a.agentState.fieldSel < len(a.agentState.fields)-1 {
				a.agentState.fieldSel++
			}
			return a, nil
		case " ", "enter":
			row := &a.agentState.fields[a.agentState.fieldSel]
			switch row.kind {
			case "toggle":
				next := "on"
				if row.value == "on" {
					next = "off"
				}
				if err := a.applyAgentFieldChange(*row, next); err != nil {
					a.agentState.errorMsg = err.Error()
				}
			case "choose":
				next := nextChoice(row.opts, row.value)
				if err := a.applyAgentFieldChange(*row, next); err != nil {
					a.agentState.errorMsg = err.Error()
				}
			case "text":
				a.agentState.fieldEdit = true
				a.agentState.errorMsg = ""
				a.editor.SetValue(row.value)
				_ = a.editor.Focus()
			}
			return a, nil
		}
		return a, nil
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
		if p := a.selectedAgentProfile(); p != nil {
			a.agentState.editMode = true
			a.agentState.fieldSel = 0
			a.agentState.errorMsg = ""
			a.agentState.fields = a.buildAgentFields(*p)
		}
		return a, nil
	}
	return a, nil
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
	changed := *p
	if err := row.set(&changed, raw); err != nil {
		return err
	}
	if _, err := agentprofile.Save(changed); err != nil {
		return err
	}
	a.loadAgentProfiles()
	for i, prof := range a.agentState.profiles {
		if prof.Name == changed.Name {
			a.agentState.selected = i
			break
		}
	}
	a.agentState.fields = a.buildAgentFields(a.agentState.profiles[a.agentState.selected])
	return nil
}

func nextChoice(opts []string, current string) string {
	idx := -1
	for i, o := range opts {
		if o == current {
			idx = i
			break
		}
	}
	return opts[(idx+1)%len(opts)]
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
