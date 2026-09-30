package agent

import (
	"regexp"
	"strings"
)

// A goal is accepted as complete only after a verification pass. A pass that
// changed files and then ran its own check — the tests, the script it wrote,
// a build — has already verified, and forcing one more pass made the model
// re-list, re-read and re-run everything it had just proven (a 2× cost on a
// small feature goal). isVerifyingBash is the harness side of that
// observation: the call ran, exited 0, and was more than an inspection.

// exitStatusLine is Bash's trailer for a non-zero exit.
var exitStatusLine = regexp.MustCompile(`(?m)^exit status [1-9][0-9]*\s*$`)

// shellSeparators splits a command line into its simple commands.
var shellSeparators = regexp.MustCompile(`&&|\|\||;|\|`)

// inspectionCommands are commands that look but prove nothing about a
// change: running one after an edit is not verification.
var inspectionCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true, "pwd": true,
	"echo": true, "printf": true, "wc": true, "find": true, "grep": true, "rg": true, "tree": true,
	"stat": true, "file": true, "du": true, "df": true, "which": true, "type": true, "whoami": true,
	"date": true, "env": true, "true": true, "sort": true, "uniq": true, "diff": true,
}

// isVerifyingBash reports whether a tool call was a successful,
// non-inspection Bash command.
func isVerifyingBash(name string, args map[string]any, result string) bool {
	if !strings.EqualFold(name, "Bash") {
		return false
	}
	if strings.HasPrefix(result, "tool result withheld:") || exitStatusLine.MatchString(result) {
		return false
	}
	cmd, _ := args["command"].(string)
	return !isInspectionOnly(cmd)
}

// isInspectionOnly reports whether every segment of a shell command is an
// inspection command (or a read-only git subcommand).
func isInspectionOnly(cmd string) bool {
	segs := shellSeparators.Split(cmd, -1)
	seen := false
	for _, seg := range segs {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		seen = true
		word := fields[0]
		if i := strings.LastIndexByte(word, '/'); i >= 0 {
			word = word[i+1:]
		}
		if word == "git" && len(fields) > 1 {
			switch fields[1] {
			case "status", "log", "diff", "show", "branch", "ls-files", "rev-parse":
				continue
			}
			return false
		}
		if !inspectionCommands[word] {
			return false
		}
	}
	return seen
}
