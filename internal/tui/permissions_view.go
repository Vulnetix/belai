package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// permissionsViewState tracks the permissions editor UI.
type permissionsViewState struct {
	selected       int
	scope          config.Scope
	mode           string // "" | "add" | "edit" | "preview"
	editIdx        int
	addDecision    string
	errorMsg       string
	previewSubject string
	gl             glState
}

// permChromeRows is what the screen spends outside the grouped body: the
// view's own padding (2), the header and its rule (2), the scope line and the
// key line.
const permChromeRows = 6

// permRow is one rendered permission rule.
type permRow struct {
	decision  string // allow | ask | deny
	rule      string
	inherited bool
	unknown   bool
	broad     bool
}

var dimStyle = lipgloss.NewStyle().Faint(true)

func (a *App) permissionRows() []permRow {
	merged := a.settings.Permissions
	own := a.ownPermissionRules()
	ownSet := map[string]bool{}
	for _, r := range own.Allow {
		ownSet[r] = true
	}
	for _, r := range own.Ask {
		ownSet[r] = true
	}
	for _, r := range own.Deny {
		ownSet[r] = true
	}

	known := knownToolNames(a.workdir)
	var rows []permRow
	for _, r := range merged.Deny {
		rows = append(rows, makePermRow("deny", r, !ownSet[r], known))
	}
	for _, r := range merged.Allow {
		rows = append(rows, makePermRow("allow", r, !ownSet[r], known))
	}
	for _, r := range merged.Ask {
		rows = append(rows, makePermRow("ask", r, !ownSet[r], known))
	}
	return rows
}

func makePermRow(decision, rule string, inherited bool, known map[string]bool) permRow {
	tool := ruleTool(rule)
	_, hasSpec := parseRuleSpec(rule)
	return permRow{
		decision:  decision,
		rule:      rule,
		inherited: inherited,
		unknown:   !known[strings.ToLower(tool)],
		broad:     !hasSpec || strings.HasSuffix(rule, "(*)"),
	}
}

func (a *App) permissionsView() string {
	rows := a.permissionRows()
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Tool Permissions", "esc back", w))
	scope := string(a.permState.scope)
	if scope == "" {
		// The view opens before a scope has been chosen; it writes project.
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) + "\n")

	// tail is what sits under the body: the preview, the editor and an error.
	var tail []string
	if a.permState.mode == "preview" {
		tail = append(tail, components.MutedStyle.Render("preview  ")+ansi.Truncate(a.permState.previewSubject, max(w-10, 8), "…"))
		if strings.TrimSpace(a.permState.previewSubject) != "" {
			tool, subject := splitPreview(a.permState.previewSubject)
			dec, rule := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny).Explain(tool, subject)
			switch {
			case rule != "":
				tail = append(tail, "         "+decisionStyle(string(dec)).Render(string(dec))+
					components.MutedStyle.Render(" via "+rule))
			case a.effectivePosture().Level(posture.PermissionNoMatch) == posture.Enforce:
				tail = append(tail, components.DangerStyle.Render("         blocked (no rule matches; permission_no_match=enforce)"))
			default:
				tail = append(tail, components.AccentStyle.Render("         allowed (no rule matches — default)"))
			}
		}
	} else if a.permState.mode != "" {
		tail = append(tail, strings.Split(a.renderFieldEditor(a.permState.mode+" rule", w), "\n")...)
	}
	if a.permState.errorMsg != "" {
		tail = append(tail, components.DangerStyle.Render(ansi.Truncate("✗ "+a.permState.errorMsg, w, "…")))
	}

	foot := glHelpLine([]glKey{
		{"↑↓", "move", 1}, {"a", "add", 1}, {"esc", "back", 1},
		{"e", "edit", 2}, {"d", "delete", 2}, {"[ ]", "group", 3},
		{"←→", "decision", 3}, {"p", "preview", 4}, {"s", "scope", 4},
	}, w)
	if a.permState.mode != "" {
		foot = components.HelpBar("enter", "save", "esc", "cancel")
	}

	groups := a.permissionGroups(rows)
	a.permState.selected = glSnap(groups, a.permState.selected)
	bodyH := 0
	if a.height > 0 {
		bodyH = max(a.height-permChromeRows-len(tail), 9)
	}
	empty := "no rules — every tool call is allowed; add a deny rule to restrict"
	b.WriteString(glBody(glSpec{groups: groups, cursor: a.permState.selected, empty: empty}, &a.permState.gl, w, bodyH))
	for _, l := range tail {
		b.WriteString("\n" + l)
	}
	b.WriteString("\n" + foot)
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// permissionGroups lists the rules under deny, allow and ask, in the order
// permissionRows builds them, so a row's idx is still its place in that list.
// The tint is the decision's colour: deny a stop, allow go, ask a pause.
func (a *App) permissionGroups(rows []permRow) []glGroup {
	titles := map[string]string{"deny": "Deny", "allow": "Allow", "ask": "Ask"}
	var groups []glGroup
	for i, r := range rows {
		if len(groups) == 0 || groups[len(groups)-1].key != r.decision {
			tint := decisionStyle(r.decision)
			groups = append(groups, glGroup{key: r.decision, title: titles[r.decision], tint: &tint})
		}
		var marks []string
		detail := []string{components.EmphStyle.Render(r.rule)}
		if r.unknown {
			marks = append(marks, "? unknown tool")
			detail = append(detail, components.MutedStyle.Render("no tool by this name is registered, so the rule never matches"))
		}
		if r.broad {
			marks = append(marks, "! broad")
			detail = append(detail, components.WarnStyle.Render("no argument limit: it covers every call of this tool"))
		}
		src := ""
		if r.inherited {
			src = "inherited"
			detail = append(detail, components.MutedStyle.Render("comes from another settings layer; edit it there"))
		}
		g := &groups[len(groups)-1]
		g.rows = append(g.rows, glRow{
			idx: i, key: r.rule, label: r.rule, value: strings.Join(marks, " · "),
			src: src, dim: r.inherited, detail: detail,
		})
	}
	return groups
}

// decisionStyle colours a permission decision: deny reads as a stop, allow as
// go, ask as a pause.
func decisionStyle(decision string) lipgloss.Style {
	switch decision {
	case "allow":
		return components.AccentStyle
	case "deny":
		return components.DangerStyle
	default:
		return components.WarnStyle
	}
}

func (a *App) handlePermissionsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.permState.mode == "add" || a.permState.mode == "edit" {
		switch m.String() {
		case "esc":
			a.permState.mode = ""
			a.permState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			raw := strings.TrimSpace(a.editor.Value())
			a.editor.Reset()
			if err := permissions.ValidateRule(raw); err != nil {
				a.permState.errorMsg = err.Error()
				return a, nil
			}
			if a.permState.mode == "add" {
				if err := a.addPermissionRule(a.permState.addDecision, raw); err != nil {
					a.permState.errorMsg = err.Error()
				} else {
					a.permState.mode = ""
					a.permState.errorMsg = ""
				}
			} else {
				rows := a.permissionRows()
				if a.permState.editIdx >= len(rows) {
					a.permState.mode = ""
					return a, nil
				}
				old := rows[a.permState.editIdx]
				if err := a.replacePermissionRule(old, raw); err != nil {
					a.permState.errorMsg = err.Error()
				} else {
					a.permState.mode = ""
					a.permState.errorMsg = ""
				}
			}
			return a, nil
		default:
			cmd := a.editor.Update(m)
			return a, cmd
		}
	}

	if a.permState.mode == "preview" {
		switch m.String() {
		case "esc":
			a.permState.mode = ""
			a.permState.previewSubject = ""
			a.editor.Reset()
			return a, nil
		default:
			cmd := a.editor.Update(m)
			a.permState.previewSubject = a.editor.Value()
			return a, cmd
		}
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k", "down", "j":
		delta := 1
		if m.String() == "up" || m.String() == "k" {
			delta = -1
		}
		groups := a.permissionGroups(a.permissionRows())
		a.permState.selected = glStep(groups, glSnap(groups, a.permState.selected), delta)
		return a, nil
	case "[", "]", "tab", "shift+tab":
		delta := 1
		if m.String() == "[" || m.String() == "shift+tab" {
			delta = -1
		}
		groups := a.permissionGroups(a.permissionRows())
		a.permState.selected = glStepGroup(groups, glSnap(groups, a.permState.selected), delta)
		a.permState.gl.scroll = 0
		return a, nil
	case "s":
		if a.permState.scope == config.ScopeGlobal {
			a.permState.scope = config.ScopeProject
		} else {
			a.permState.scope = config.ScopeGlobal
		}
		return a, nil
	case "a":
		a.permState.mode = "add"
		a.permState.addDecision = "deny"
		a.permState.errorMsg = ""
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	case "left", "h", "right", "l":
		rows := a.permissionRows()
		if a.permState.selected < len(rows) {
			row := rows[a.permState.selected]
			if row.inherited {
				a.permState.errorMsg = "inherited rules cannot be edited here"
				return a, nil
			}
			if err := a.cycleDecision(row); err != nil {
				a.permState.errorMsg = err.Error()
			} else {
				a.permState.errorMsg = ""
			}
		}
		return a, nil
	case "e":
		rows := a.permissionRows()
		if a.permState.selected >= len(rows) {
			return a, nil
		}
		row := rows[a.permState.selected]
		if row.inherited {
			a.permState.errorMsg = "inherited rules cannot be edited here"
			return a, nil
		}
		a.permState.mode = "edit"
		a.permState.editIdx = a.permState.selected
		a.permState.errorMsg = ""
		a.editor.SetValue(row.rule)
		_ = a.editor.Focus()
		return a, nil
	case "d":
		rows := a.permissionRows()
		if a.permState.selected < len(rows) {
			row := rows[a.permState.selected]
			if row.inherited {
				a.permState.errorMsg = "inherited rules cannot be deleted here"
				return a, nil
			}
			if err := a.deletePermissionRule(row); err != nil {
				a.permState.errorMsg = err.Error()
			} else {
				a.permState.errorMsg = ""
				if a.permState.selected >= len(a.permissionRows()) {
					a.permState.selected = 0
				}
			}
		}
		return a, nil
	case "p":
		a.permState.mode = "preview"
		a.permState.previewSubject = ""
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	}

	cmd := a.editor.Update(m)
	return a, cmd
}

func (a *App) ownPermissionRules() config.PermissionRules {
	if a.permState.scope == config.ScopeGlobal {
		g, _ := config.LoadGlobal()
		return g.Permissions
	}
	p, _ := config.LoadProject(a.workdir)
	return p.Permissions
}

func (a *App) mutatePermissions(fn func(*config.PermissionRules)) error {
	if err := config.Mutate(a.permState.scope, a.workdir, func(s *config.Settings) error {
		fn(&s.Permissions)
		return nil
	}); err != nil {
		return err
	}
	return a.reloadSettings()
}

func (a *App) addPermissionRule(decision, rule string) error {
	return a.mutatePermissions(func(p *config.PermissionRules) {
		switch decision {
		case "ask":
			p.Ask = append(p.Ask, rule)
		case "allow":
			p.Allow = append(p.Allow, rule)
		default:
			p.Deny = append(p.Deny, rule)
		}
	})
}

func (a *App) deletePermissionRule(row permRow) error {
	return a.mutatePermissions(func(p *config.PermissionRules) {
		p.Allow = removeString(p.Allow, row.rule)
		p.Ask = removeString(p.Ask, row.rule)
		p.Deny = removeString(p.Deny, row.rule)
	})
}

func (a *App) replacePermissionRule(row permRow, newRule string) error {
	return a.mutatePermissions(func(p *config.PermissionRules) {
		p.Allow = replaceString(p.Allow, row.rule, newRule)
		p.Ask = replaceString(p.Ask, row.rule, newRule)
		p.Deny = replaceString(p.Deny, row.rule, newRule)
	})
}

func (a *App) cycleDecision(row permRow) error {
	return a.mutatePermissions(func(p *config.PermissionRules) {
		p.Allow = removeString(p.Allow, row.rule)
		p.Ask = removeString(p.Ask, row.rule)
		p.Deny = removeString(p.Deny, row.rule)
		switch row.decision {
		case "deny":
			p.Ask = append(p.Ask, row.rule)
		case "ask":
			p.Allow = append(p.Allow, row.rule)
		default:
			p.Deny = append(p.Deny, row.rule)
		}
	})
}

func ruleTool(rule string) string {
	tool, _, _ := splitRule(rule)
	return tool
}

func parseRuleSpec(rule string) (spec string, hasSpec bool) {
	_, spec, hasSpec = splitRule(rule)
	return spec, hasSpec
}

func splitRule(rule string) (tool, spec string, hasSpec bool) {
	i := strings.Index(rule, "(")
	if i >= 0 && strings.HasSuffix(rule, ")") {
		return rule[:i], rule[i+1 : len(rule)-1], true
	}
	return rule, "", false
}

func knownToolNames(workdir string) map[string]bool {
	out := map[string]bool{}
	for _, n := range tools.Default(workdir, false).Names() {
		out[strings.ToLower(n)] = true
	}
	return out
}

func splitPreview(s string) (tool, subject string) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " "); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func replaceString(list []string, old, new string) []string {
	for i, v := range list {
		if v == old {
			list[i] = new
		}
	}
	return list
}
