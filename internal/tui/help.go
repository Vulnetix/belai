package tui

import "strings"

// The help body is assembled from two sources: the command registry, which
// already knows every slash command, and the keySections table below, which is
// the single user-facing inventory of key bindings. The bindings themselves
// live in the `switch m.String()` blocks of app.go and the view files; this
// table mirrors them, so a new binding wants a line here too.

// keyBinding is one key and what it does in a given context.
type keyBinding struct {
	Keys string
	Desc string
}

// keySection groups bindings by the screen or mode they apply to.
type keySection struct {
	Title    string
	Bindings []keyBinding
}

// keySections returns every keyboard shortcut the TUI handles, grouped by
// context and ordered from the most global to the most specific.
func keySections() []keySection {
	return []keySection{
		{"anywhere", []keyBinding{
			{"ctrl+c", "copy the prompt — or the hovered panel — to the clipboard"},
			{"ctrl+d", "exit — press twice; esc cancels"},
			{"ctrl+r", "cycle reasoning display: auto, shown, hidden"},
			{"ctrl+t", "cycle tool-call display: auto → all → edits only → none"},
			{"f2", "toggle caveman voice rewrite"},
			{"f3", "toggle guardrails"},
			{"f4", "toggle ask permission"},
			{"f5", "cycle operating mode"},
			{"f6", "cycle reasoning effort"},
			{"f8", "open the runs panel on the subagents tab (chat)"},
			{"f9", "toggle the runs panel on the activity tab (chat)"},
			{"f10", "toggle the Vulnetix AI Firewall (chat)"},
		}},
		{"chat", []keyBinding{
			{"enter", "send; also runs !shell, !!process, and /commands, or steers a running turn"},
			{"ctrl+j, shift+enter", "newline"},
			{"ctrl+left, ctrl+right", "move the cursor one word left or right"},
			{"home, end", "jump to the start or end of the line (fn+left, fn+right)"},
			{"esc", "clear the selection, then cancel the request; esc esc clears the composer"},
			{"shift+tab", "cycle mode: agent, plan, goal"},
			{"up", "browse prompt history and the prompt library"},
			{"f7", "save the prompt to the library"},
			{"ctrl+l", "clear the transcript view; the session is kept"},
			{"ctrl+o", "expand or collapse every truncated output"},
			{"ctrl+s", "save the hovered panel to a path, overwrite/delete a loaded library prompt, or save the prompt to the library"},
			{"ctrl+x", "copy the session id to the clipboard"},
		}},
		{"slash completions (while the / popup is open)", []keyBinding{
			{"tab", "highlight the next completion, wrapping at the end"},
			{"enter", "put the highlighted completion in the prompt; enter again sends it"},
			{"right", "put the highlighted completion, or the first one, in the prompt"},
			{"esc", "drop the highlight, keeping the popup"},
		}},
		{"file chooser (@, above the prompt)", []keyBinding{
			{"@", "open the file chooser in any mode"},
			{"type", "filter the file list; the @prefix is the filter"},
			{"up, down", "move the highlight, wrapping"},
			{"right, tab, enter", "insert the highlighted path and close the chooser"},
			{"esc, left", "close the chooser; typing reopens it"},
		}},
		{"add-dir chooser (/add-dir)", []keyBinding{
			{"/add-dir", "open the directory chooser; the filter is the prompt"},
			{"type", "filter the directory list"},
			{"up, down", "move the highlight, wrapping"},
			{"enter", "add the highlighted directory"},
			{"y, n", "confirm or reject the highlighted directory"},
			{"esc", "cancel and return to the prompt"},
		}},
		{"agent picker (agent mode, above the prompt)", []keyBinding{
			{"/agent", "open the agent picker; bare @ opens files"},
			{"enter", "open the picker in agent mode when no agent is engaged"},
			{"tab", "highlight the next agent, ending on (none)"},
			{"enter", "engage the highlighted agent for the following agent-mode turns"},
			{"right", "engage the highlighted agent"},
			{"ctrl+g", "start the highlighted ↻ definition as a background agent"},
			{"esc", "close the picker"},
		}},
		{"agent name completion (/agent start … and friends)", []keyBinding{
			{"/agent start ", "list the profiles that can be started; stop, pause, resume and log list the agents already running"},
			{"type", "narrow the list to names with that prefix"},
			{"tab", "highlight the next name; nothing is highlighted until you press it"},
			{"enter, right", "complete the line with the highlighted name and run it"},
		}},
		{"transcript", []keyBinding{
			{"pgup, pgdown", "page up, page down"},
			{"shift+up, shift+down", "scroll one line"},
			{"ctrl+home, ctrl+end", "jump to the top, jump to the bottom"},
			{"wheel", "scroll and detach from the tail"},
			{"drag", "select text; release copies it"},
		}},
		{"runs panel (after f8/f9)", []keyBinding{
			{"tab", "switch between activity and subagents tabs"},
			{"up, down", "select an item"},
			{"enter", "activity: send output; subagents: filter transcript"},
			{"v", "activity: view the selected output full-screen"},
			{"x", "activity: kill; subagents: cancel or dismiss"},
			{"t", "activity: start the triage agent on the selected project"},
			{"esc", "unfocus the panel (panel stays open)"},
			{"f9", "close the panel"},
		}},
		{"plan review (after a plan-mode turn)", []keyBinding{
			{"up, down", "move between approve, refine and cancel"},
			{"enter", "confirm the highlighted action"},
			{"pgup, pgdown", "page the plan up, page down"},
			{"shift+up, shift+down", "scroll the plan one line"},
			{"ctrl+home, ctrl+end", "jump to the top, bottom of the plan"},
			{"wheel", "scroll the plan"},
			{"esc", "cancel; the plan file is kept"},
		}},
		{"prompt history (after up)", []keyBinding{
			{"up, down", "older result, newer result"},
			{"tab", "load the next named prompt from the strip, wrapping at the end"},
			{"right", "accept the loaded prompt into the composer, leaving the cycle"},
			{"enter", "accept and send"},
			{"esc", "cancel and restore what you typed"},
			{"type", "leave the cycle and edit the loaded prompt"},
		}},
		{"save prompt (after f7)", []keyBinding{
			{"enter", "save under the typed name; empty cancels"},
			{"esc", "cancel"},
		}},
		{"/model", []keyBinding{
			{"up, down", "move between rows"},
			{"⏎", "edit the selected row"},
			{"s", "cycle scope for the active role"},
			{"c", "clear the selected row"},
			{"p", "open /providers"},
			{"esc", "back"},
		}},
		{"model picker", []keyBinding{
			{"up, down", "previous model, next model"},
			{"/", "filter the model list"},
			{"enter", "use this model"},
			{"backspace", "delete a filter character"},
			{"esc", "cancel"},
		}},
		{"/providers", []keyBinding{
			{"up, down", "move"},
			{"enter", "open the selected provider"},
			{"/", "filter the provider list"},
			{"p", "local server report"},
			{"i", "import discovered credentials"},
			{"r", "refetch model catalogues and availability"},
			{"esc", "back"},
		}},
		{"provider detail — credentials", []keyBinding{
			{"left, right", "cycle tabs"},
			{"up, down", "move between fields"},
			{"s", "set value"},
			{"e", "set env var reference"},
			{"c", "clear value"},
			{"b", "cycle storage backend"},
			{"i", "import discovered credentials"},
			{"esc", "back"},
		}},
		{"provider detail — server", []keyBinding{
			{"d", "download a model"},
			{"l", "launch a model"},
			{"x", "stop the server"},
			{"p", "probe the local server status"},
			{"esc", "back"},
		}},
		{"credential import", []keyBinding{
			{"up, down", "move"},
			{"space", "select or deselect"},
			{"v", "store as an env var reference"},
			{"o", "overwrite existing credentials"},
			{"b", "cycle the storage backend"},
			{"s", "toggle scope"},
			{"r", "rescan"},
			{"enter", "import"},
			{"y, n", "confirm or cancel an overwrite"},
			{"esc", "back"},
		}},
		{"/settings", []keyBinding{
			{"up, down", "move"},
			{"space, enter", "open the submenu, or edit the value"},
			{"x", "unset"},
			{"s", "toggle scope: global, project"},
			{"esc", "back"},
		}},
		{"/permissions", []keyBinding{
			{"up, down", "move"},
			{"a", "add a rule"},
			{"left, right", "cycle the decision: allow, ask, deny"},
			{"e", "edit the rule"},
			{"d", "delete the rule"},
			{"s", "toggle scope: global, project"},
			{"p", "preview which rule matches a subject"},
			{"esc", "back"},
		}},
		{"prompt library (/prompts)", []keyBinding{
			{"up, down", "move (also k, j)"},
			{"space", "toggle the selected prompt on or off"},
			{"J, K", "reorder the selected prompt later or earlier"},
			{"e", "open the selected prompt in $VISUAL/$EDITOR"},
			{"a", "create a new prompt and open it in the editor"},
			{"d", "delete the selected prompt (confirm)"},
			{"s", "toggle scope: global, project"},
			{"esc", "back"},
		}},
		{"process library (/processes)", []keyBinding{
			{"up, down", "move (also k, j)"},
			{"space", "toggle the selected process on or off"},
			{"J, K", "reorder the selected process later or earlier"},
			{"e", "open the selected process command in $VISUAL/$EDITOR"},
			{"a", "create a new process and open it in the editor"},
			{"d", "delete the selected process (confirm)"},
			{"s", "toggle scope: global, project"},
			{"r", "run the selected process"},
			{"x", "stop the selected process"},
			{"enter", "show the running process log tail"},
			{"esc", "back"},
		}},
		{"prompt action bar (after ctrl+s on a loaded prompt)", []keyBinding{
			{"enter", "overwrite the loaded prompt (confirm)"},
			{"d", "delete the loaded prompt (confirm)"},
			{"y, n", "confirm or cancel an overwrite or delete"},
			{"esc", "close the action bar"},
		}},
		{"clarifying questions", []keyBinding{
			{"up, down", "move"},
			{"space", "select or deselect"},
			{"n", "add a note"},
			{"s", "skip this question"},
			{"enter", "submit the answers"},
			{"esc", "cancel the clarification"},
		}},
		{"background agents (/agent list)", []keyBinding{
			{"up, down", "move"},
			{"enter, e", "edit the selected agent"},
			{"n", "create a new agent from a valid stub"},
			{"d", "duplicate the selected agent (built-ins become editable copies)"},
			{"esc", "back"},
		}},
		{"/vulnetix configure", []keyBinding{
			{"r", "re-probe the CLI"},
			{"l", "open scan history"},
			{"esc", "back"},
		}},
		{"/vulnetix list", []keyBinding{
			{"↑↓", "move"},
			{"/", "filter"},
			{"enter", "open artifacts for selected project"},
			{"r", "re-sweep"},
			{"c", "configure"},
			{"esc", "back"},
		}},
		{"/vulnetix artifacts", []keyBinding{
			{"↑↓", "move"},
			{"t", "start triage agent"},
			{"l", "open history"},
			{"esc", "back"},
		}},
		{"agent editor", []keyBinding{
			{"up, down", "move"},
			{"left, right", "cycle a choose or toggle value in place"},
			{"space, enter", "cycle a choose/toggle, open text or system prompt, or open tools"},
			{"ctrl+j", "insert a newline in the system-prompt editor"},
			{"e", "edit the system prompt in $VISUAL/$EDITOR"},
			{"n", "create a new agent from a valid stub"},
			{"d", "duplicate the selected agent (built-ins become editable copies)"},
			{"esc", "back"},
		}},
		{"agent tools picker", []keyBinding{
			{"up, down", "move"},
			{"space", "toggle the selected tool"},
			{"a", "select all tools"},
			{"n", "select no tools (inherit)"},
			{"enter", "commit the sorted selection"},
			{"esc", "cancel"},
		}},
		{"text entry (any prompt, note, or rule field)", []keyBinding{
			{"enter", "commit"},
			{"esc", "cancel"},
		}},
	}
}

// helpText renders the /help body: every slash command the registry exposes,
// then every key binding grouped by context. The list views also accept the
// vim keys h, j, k and l in place of the arrows.
func helpText(r *Registry) string {
	var b strings.Builder

	b.WriteString("commands:")
	names := r.Names()
	width := 0
	for _, n := range names {
		width = max(width, len(n)+1)
	}
	for _, n := range names {
		c, ok := r.Command(n)
		if !ok {
			continue
		}
		desc := c.Description
		if c.AliasOf != "" {
			desc = "alias of /" + c.AliasOf
		}
		b.WriteString("\n  " + pad("/"+n, width) + " — " + desc)
	}

	for _, s := range keySections() {
		width = 0
		for _, k := range s.Bindings {
			width = max(width, len(k.Keys))
		}
		b.WriteString("\n\n" + s.Title + ":")
		for _, k := range s.Bindings {
			b.WriteString("\n  " + pad(k.Keys, width) + " — " + k.Desc)
		}
	}

	b.WriteString("\n\nlist views also take h, j, k and l for the arrows.")
	b.WriteString("\nin the agent picker, ◈ marks a built-in and ↻ a background-agent definition.")
	return b.String()
}

// pad right-pads s with spaces to width, so the em dashes line up.
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
