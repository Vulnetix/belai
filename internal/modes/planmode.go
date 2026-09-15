package modes

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/session"
)

// PlanOption is the user's choice after a plan is extracted.
type PlanOption string

const (
	PlanExecute PlanOption = "execute"
	PlanStay    PlanOption = "stay"
	PlanRefine  PlanOption = "refine"
)

// PlanRoute is the outcome of routing a plan option.
type PlanRoute struct {
	Mode   Mode
	Action string // execute, stay, refine
}

// RoutePlanOption maps a user selection to the resulting mode. Execute leaves
// plan mode (full tools restored); Stay and Refine remain in plan mode.
func RoutePlanOption(opt PlanOption) (PlanRoute, error) {
	switch opt {
	case PlanExecute:
		return PlanRoute{Mode: ModeAgent, Action: "execute"}, nil
	case PlanStay:
		return PlanRoute{Mode: ModePlan, Action: "stay"}, nil
	case PlanRefine:
		return PlanRoute{Mode: ModePlan, Action: "refine"}, nil
	default:
		return PlanRoute{}, fmt.Errorf("unknown plan option %q", opt)
	}
}

// Todo is one tracked plan step.
type Todo struct {
	N    int    `json:"n"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// PlanState is the persisted plan-mode state (enabled / executing / todos). It
// is stored as a session entry so it survives resume.
type PlanState struct {
	Enabled   bool   `json:"enabled"`
	Executing bool   `json:"executing"`
	Todos     []Todo `json:"todos"`
}

// ToEntry renders PlanState as a session entry.
func (s PlanState) ToEntry(parentID string) session.Entry {
	data, err := json.Marshal(s)
	if err != nil {
		data = []byte("{}")
	}
	return session.Entry{
		ID:       session.MustID(),
		ParentID: parentID,
		Type:     "plan_state",
		Content:  string(data),
	}
}

// PlanStateFromEntry parses a session entry back into PlanState.
func PlanStateFromEntry(e session.Entry) (PlanState, error) {
	var s PlanState
	if err := json.Unmarshal([]byte(e.Content), &s); err != nil {
		return PlanState{}, fmt.Errorf("parse plan state: %w", err)
	}
	return s, nil
}

// Progress builds a plans.Progress from the tracked todos.
func (s PlanState) Progress() *plans.Progress {
	p := plans.NewProgress(len(s.Todos))
	for _, t := range s.Todos {
		if t.Done {
			p.MarkDone(t.N)
		}
	}
	return p
}

// writeTools are built-in edit/write tools disabled in plan mode.
var writeTools = map[string]bool{
	"write":         true,
	"edit":          true,
	"apply_patch":   true,
	"patch":         true,
	"create_file":   true,
	"delete_file":   true,
	"rename_file":   true,
	"move_file":     true,
	"copy_file":     true,
	"replace":       true,
	"remove":        true,
	"notebook_edit": true,
	"insert":        true,
}

// IsWriteTool reports whether a tool is a built-in edit/write tool. Tool
// names are case-folded so a registered "Bash" cannot bypass a lowercase key.
func IsWriteTool(name string) bool { return writeTools[strings.ToLower(name)] }

// readOnlyBash is the plan-mode bash allowlist: read-only inspection/search/
// directory/read-only-git/system-info commands. Anything else is blocked.
var readOnlyBash = map[string]bool{
	"cat": true, "grep": true, "egrep": true, "rg": true, "find": true,
	"ls": true, "uname": true, "pwd": true, "head": true, "tail": true,
	"wc": true, "sort": true, "uniq": true, "file": true, "which": true,
	"diff": true, "stat": true, "du": true, "basename": true,
	"dirname": true, "realpath": true, "readlink": true,
}

// bashMetacharacters are shell syntax that would let a command escape the
// allowlist. The bash tool never executes through a shell, but rejecting these
// before tokenising keeps the gate honest and fails closed.
const bashMetacharacters = ";&|$`<>\n()"

// findUnsafeOptions are find flags that write, delete, or execute. A read-only
// find may not carry them.
var findUnsafeOptions = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-delete": true, "-fprint": true, "-fls": true, "-fprintf": true,
}

// gitReadSubcommands is the read-only git subcommand allowlist. add/commit/
// push and other mutating subcommands are blocked.
var gitReadSubcommands = map[string]bool{
	"status":    true,
	"log":       true,
	"diff":      true,
	"show":      true,
	"rev-parse": true,
	"ls-files":  true,
	"grep":      true,
	"describe":  true,
}

// BashAllowed reports whether a bash command is within the read-only allowlist.
// Shell metacharacters are rejected before tokenising, so "cat x; rm -rf /"
// can never pass by merely starting with an allowlisted word.
func BashAllowed(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, bashMetacharacters) {
		return false
	}
	fields := strings.Fields(command)
	base := filepath.Base(fields[0])
	switch base {
	case "git":
		return gitReadOnly(fields)
	case "find":
		return findReadOnly(fields)
	default:
		return readOnlyBash[base]
	}
}

// findReadOnly rejects find invocations that can write, delete, or execute.
func findReadOnly(fields []string) bool {
	for _, f := range fields[1:] {
		if findUnsafeOptions[f] {
			return false
		}
	}
	return true
}

// gitValueOptions are git options that consume a following argument.
var gitValueOptions = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--config-env": true,
}

func gitReadOnly(fields []string) bool {
	for i := 1; i < len(fields); i++ {
		f := fields[i]
		if gitValueOptions[f] {
			i++ // skip the option's argument
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue
		}
		return gitReadSubcommands[f]
	}
	return false
}

// ToolAllowed reports whether a tool call may proceed. In plan mode write
// tools are disabled and bash is restricted to the read-only allowlist; other
// tools remain active. Names are case-folded.
func ToolAllowed(name string, args map[string]any, planEnabled bool) bool {
	if !planEnabled {
		return true
	}
	if IsWriteTool(name) {
		return false
	}
	if strings.EqualFold(name, "bash") {
		cmd, _ := args["command"].(string)
		return BashAllowed(cmd)
	}
	return true
}
