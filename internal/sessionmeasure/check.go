package sessionmeasure

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testrun"
)

// EntryCheckRun is the session entry type of one check run.
const EntryCheckRun = "check_run"

// Check is one build, test, lint or gate run and how it ended.
type Check struct {
	Kind       Kind
	Program    string
	Subcommand string
	ExitCode   int
	DurationMS int64
}

// OK reports whether the check passed: it exited 0.
func (c Check) OK() bool { return c.ExitCode == 0 }

// Meta is the entry's meta, the wire shape vdb-site reads:
//
//	{"kind":"test","program":"go","subcommand":"test","exit_code":0,"duration_ms":4210,"ok":true}
func (c Check) Meta() map[string]any {
	return map[string]any{
		"kind":        string(c.Kind),
		"program":     c.Program,
		"subcommand":  c.Subcommand,
		"exit_code":   c.ExitCode,
		"duration_ms": c.DurationMS,
		"ok":          c.OK(),
	}
}

// Entry is the check as a session entry. It has no content: the figures are
// the whole record, and a viewer that does not know the type shows nothing.
func (c Check) Entry() session.Entry {
	return session.Entry{Type: EntryCheckRun, Role: "system", Meta: c.Meta()}
}

var programRE = regexp.MustCompile(`^[a-z][a-z0-9._+-]{0,63}$`)

// New builds the check for argv that ran for dur and exited with exit. A
// caller that knows what the command was (the post-end test pass runs test
// suites, a gate run is a gate) passes kind; the classifier then supplies only
// the program and subcommand. With kind empty the argv must classify, and
// New reports false when it does not.
func New(kind Kind, argv []string, exit int, dur time.Duration) (Check, bool) {
	if len(argv) == 0 {
		return Check{}, false
	}
	cls, known := Classify(argv)
	if kind == "" {
		if !known {
			return Check{}, false
		}
		kind = cls.Kind
	}
	if !known {
		cls.Program = strings.ToLower(path.Base(argv[0]))
		if !programRE.MatchString(cls.Program) {
			cls.Program = ""
		}
		cls.Subcommand = ""
	}
	return Check{Kind: kind, Program: cls.Program, Subcommand: cls.Subcommand, ExitCode: exit, DurationMS: dur.Milliseconds()}, true
}

// FromTestrun is the check for one suite run. A run that never executed (a
// deny or ask rule, a missing binary, no sandbox) is not a check, so it
// reports false. A timeout records exit code -1.
func FromTestrun(r testrun.Result, kind Kind) (Check, bool) {
	switch r.Status {
	case testrun.Passed, testrun.Failed, testrun.TimedOut:
	default:
		return Check{}, false
	}
	exit := r.ExitCode
	if r.Status == testrun.TimedOut {
		exit = -1
	}
	return New(kind, r.Command, exit, r.Duration)
}

var exitStatusRE = regexp.MustCompile(`(?m)^exit status (-?\d+)\s*$`)

// bashTimedOut is the line the Bash tool appends when it kills a command at
// its timeout (internal/tools/bash.go).
const bashTimedOut = "… command timed out after"

// BashExit reads how a Bash tool result ended. The tool appends `exit status
// N` to the output of a command that failed and a timeout line to one it
// killed; a result with neither exited 0. ok is false for a result that says
// nothing about the command, such as one withheld by the harness.
func BashExit(content string) (exit int, ok bool) {
	if strings.HasPrefix(content, "tool result withheld:") {
		return 0, false
	}
	if i := strings.LastIndex(content, bashTimedOut); i >= 0 && (i == 0 || content[i-1] == '\n') {
		return -1, true
	}
	if m := exitStatusRE.FindAllStringSubmatch(content, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return n, true
		}
	}
	return 0, true
}

// FromBash is the check for one finished shell command: a Bash tool call or a
// `!cmd` line. It reports false when the line is not a known check or its
// result says nothing about how it ended. The caller passes the whole result,
// before any cap on what is persisted, because the exit line is at the end.
func FromBash(command, content string, durMS int64) (Check, bool) {
	cls, ok := ClassifyLine(command)
	if !ok {
		return Check{}, false
	}
	exit, ok := BashExit(content)
	if !ok {
		return Check{}, false
	}
	return Check{Kind: cls.Kind, Program: cls.Program, Subcommand: cls.Subcommand, ExitCode: exit, DurationMS: max(durMS, 0)}, true
}

// FromBashArgs is FromBash for a Bash tool call, whose arguments are the JSON
// object the model sent.
func FromBashArgs(args, content string, durMS int64) (Check, bool) {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(args), &a) != nil || strings.TrimSpace(a.Command) == "" {
		return Check{}, false
	}
	return FromBash(a.Command, content, durMS)
}

// IsBash reports whether a tool name is the shell tool.
func IsBash(name string) bool { return strings.EqualFold(name, "Bash") }
