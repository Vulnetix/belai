package tools

import (
	"context"
	"errors"
	"strings"
)

// CodeName is the code-mode script tool's name.
const CodeName = "Code"

// CodeRunner runs one script for the session and returns the harness-composed
// output. The session supplies it: every nested tool call the script makes
// goes back through the session's own gate pipeline, so the tool holds no
// permission logic of its own.
type CodeRunner func(ctx context.Context, script string) (output string, err error)

// Code runs a script in the code-mode interpreter (internal/codemode,
// docs/code-mode.md). It exists only on the code-mode surface: the session
// adds it to the registry code mode uses and to no other, so agent, plan and
// goal mode never advertise or resolve it.
//
// It is KindCode and does not itself mutate: each nested call asks and is
// judged on its own merits, so asking for the script as well would only ask
// twice. A Deny rule on "Code" still withholds the whole script.
type Code struct {
	Run CodeRunner
}

// Definition describes the tool. The text is the API reference the model
// needs; it is the only per-turn cost of the mode beyond the tool schemas.
func (Code) Definition() Definition {
	return Definition{
		Name: CodeName,
		Description: "Run a JavaScript script that calls tools as functions and prints a result. Use it to batch work that would take many tool calls: " +
			"loops, filters, reading or searching many files, editing several places, or combining MCP tools. Only what the script prints (print(...)) or returns comes back, " +
			"so intermediate data never enters the conversation. Use the normal tools for a single read or edit.\n" +
			"Globals: tools.<Name>(args) returns the tool's result as a string (arguments are the same as the tool's own, e.g. tools.Grep({pattern: \"TODO\"})); " +
			"tools.parallel([[\"Read\",{file_path:\"a\"}],[\"Grep\",{pattern:\"x\"}]]) runs read-only calls together and returns an array of strings; " +
			"tools.list(), tools.search(query), tools.describe(name) discover tools; mcp.list(), mcp.describe(\"server.tool\"), mcp.<server>.<tool>(args) call MCP tools; " +
			"print(...) and JSON are available. There is no filesystem, network, require or timer. A tool result that was withheld comes back as its withheld message. " +
			"Every call is permission-checked and asks like a direct call. A script has a time limit, a call budget and an output cap; print summaries, not whole files.",
		Properties: map[string]Property{
			"code": {Type: "string", Description: "The script body. It runs inside a function, so it may use return. Example: var r = tools.Grep({pattern: \"func main\"}); print(r.split(\"\\n\").slice(0, 20).join(\"\\n\"));"},
		},
		Required: []string{"code"},
	}
}

// Kind is KindCode.
func (Code) Kind() Kind { return KindCode }

// Mutates is false: the script's nested calls carry the mutation and its asks.
func (Code) Mutates() bool { return false }

// Subject has no permission subject, so a rule is written as "Code".
func (Code) Subject(map[string]any) string { return "" }

// Execute runs the script.
func (c Code) Execute(ctx context.Context, args map[string]any) (Result, error) {
	script, _ := args["code"].(string)
	if strings.TrimSpace(script) == "" {
		return Result{}, errors.New("code is required")
	}
	if c.Run == nil {
		return Result{}, errors.New("code mode is not available in this session")
	}
	out, err := c.Run(ctx, script)
	switch {
	case err != nil && out != "":
		out += "\n\n[" + err.Error() + "]"
	case err != nil:
		out = "[" + err.Error() + "]"
	case out == "":
		out = "(the script printed nothing; use print(...) or return a value)"
	}
	return Result{Kind: KindCode, Content: out}, nil
}

// nestedKinds is the closed set of kinds a script may call through tools.*.
// Fail-closed: a new kind is unreachable from a script until it is added here.
// Interactive, planning, board, process-control and subagent tools are absent
// by design, and MCP is reached only through the mcp namespace.
var nestedKinds = map[Kind]bool{
	KindRead: true, KindGrep: true, KindGlob: true, KindEdit: true, KindWrite: true,
	KindBash: true, KindWebFetch: true, KindWebSearch: true, KindNative: true,
	KindRemote: true, KindFetched: true,
}

// nestedExcluded names tools whose kind is otherwise reachable but which a
// script must not call: they ask the user, plan, spawn agents or load
// instructions, none of which is a script's to drive.
var nestedExcluded = map[string]bool{
	CodeName: true, "Task": true, "AskUserQuestion": true, "ExitPlanMode": true,
	"update_plan": true, ToolSearchName: true, "Skill": true, "SkillDraft": true,
}

// NestedAllowed reports whether a script may call t as tools.<Name>: a kind
// on the nested allowlist and a name off the exclusion list.
func NestedAllowed(t Tool) bool {
	return nestedKinds[t.Kind()] && !nestedExcluded[t.Definition().Name]
}

// CodeSurface returns the registry code mode advertises: r without MCP tools,
// plus the Code tool. r is never changed, so the other modes' registries stay
// byte-for-byte what they were.
func (r *Registry) CodeSurface(code *Code) *Registry {
	list := make([]Tool, 0, len(r.tools)+1)
	for _, t := range r.tools {
		if t.Kind() == KindMCP {
			continue
		}
		list = append(list, t)
	}
	list = append(list, code)
	return r.withCwd(NewRegistry(list...))
}

// NestedTools returns the tools a script may call as tools.<Name>, and the
// MCP tools it may call as mcp.<server>.<tool>, both drawn from r (so a
// profile's allowlist has already narrowed them).
func (r *Registry) NestedTools() (builtin []Tool, mcp []Tool) {
	for _, t := range r.tools {
		switch k := t.Kind(); {
		case k == KindMCP:
			mcp = append(mcp, t)
		case NestedAllowed(t):
			builtin = append(builtin, t)
		}
	}
	return builtin, mcp
}

type nestedKey struct{}

// WithNested marks ctx as belonging to a nested script call. The agent uses
// it to resolve MCP names (which a direct call in code mode cannot) and to
// keep a nested result whole: it is consumed by the script, never delivered
// to the model, so it is neither offloaded nor recorded in the read index.
func WithNested(ctx context.Context) context.Context {
	return context.WithValue(ctx, nestedKey{}, true)
}

// IsNested reports whether ctx belongs to a nested script call.
func IsNested(ctx context.Context) bool {
	v, _ := ctx.Value(nestedKey{}).(bool)
	return v
}
