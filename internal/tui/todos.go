package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/tui/components"
)

// todosVisible gates the panel on the ui.show_todos setting and on there being
// something to show. An empty list renders nothing.
func (a *App) todosVisible() bool {
	return a.settings.TodosVisible() && a.todos != nil && len(a.todos.Items) > 0
}

// todoPanelHeight returns the rendered height of the todo panel, or 0 when it
// is hidden or empty. It must be part of chromeHeight or the viewport gets the
// wrong height.
func (a *App) todoPanelHeight() int {
	if !a.todosVisible() {
		return 0
	}
	return lipgloss.Height(components.TodoPanel{Width: a.contentWidth(), List: a.todos}.View())
}

// renderTodoPanel draws the todo panel.
func (a *App) renderTodoPanel() string {
	if !a.todosVisible() {
		return ""
	}
	return components.TodoPanel{Width: a.contentWidth(), List: a.todos}.View()
}

// setTodos records the shared todo list and persists it as a todo_list entry.
// It is the single write path, so a list change is never rendered without
// being durable.
func (a *App) setTodos(list *todos.List) {
	if list == nil {
		return
	}
	a.todos = list
	a.appendEntry(list.ToEntry(""))
	a.refreshFooter()
}

// rehydrateTodos restores the todo list from the current session's entries on
// load, using the same latest-wins read the store uses for names and plan
// state. It is a no-op when the session has no todo_list entry yet.
func (a *App) rehydrateTodos(entries []session.Entry) {
	if list, ok := todos.Latest(entries); ok {
		a.todos = &list
	}
}
