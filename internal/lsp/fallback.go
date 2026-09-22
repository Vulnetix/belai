package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vulnetix/signet/internal/proc"
)

var (
	loc3Re = regexp.MustCompile(`^(.+?):(\d+):(\d+):\s*(.+)$`)                        // file:line:col: msg
	loc2Re = regexp.MustCompile(`^(.+?):(\d+):\s*(.+)$`)                              // file:line: msg
	bashRe = regexp.MustCompile(`^(.+?):\s*line\s*(\d+):\s*(.+)$`)                    // bash -n
	pyRe   = regexp.MustCompile(`^(\w+):\s*(.+)\s*\(\s*(.+?),?\s*line\s*(\d+)\s*\)$`) // python
)

// canFallback reports whether an honest fallback exists for path in lang.
func canFallback(lang *Language, path string, root string) bool {
	if lang.ID == "ts" {
		ext := strings.ToLower(filepath.Ext(path))
		// node --check only understands plain JS files.
		switch ext {
		case ".js", ".mjs", ".cjs":
			return true
		default:
			return false
		}
	}
	if lang.ID == "c" || lang.ID == "cpp" || lang.ID == "objc" {
		cc := filepath.Join(root, "compile_commands.json")
		if _, err := os.Stat(cc); err != nil {
			return false
		}
	}
	return len(lang.Fallback) > 0
}

// runFallback executes the fixed-argv syntax checker for lang and path.
func runFallback(ctx context.Context, lang *Language, path string) Report {
	if len(lang.Fallback) == 0 {
		return Report{Language: lang.Display, Status: StatusUnavailable}
	}
	argv := make([]string, len(lang.Fallback))
	for i, a := range lang.Fallback {
		if a == "%s" {
			argv[i] = path
		} else {
			argv[i] = a
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = proc.ScrubbedEnv()
	cmd.Dir = filepath.Dir(path)
	proc.SetProcessGroup(cmd)
	out, _ := cmd.CombinedOutput()
	rows := parseFallbackOutput(lang, string(out), path)
	return Report{Language: lang.Display, Status: StatusFallback, Rows: rows}
}

// parseFallbackOutput selects the right parser for lang.
func parseFallbackOutput(lang *Language, output, path string) []Row {
	switch lang.ID {
	case "bash":
		return parseBash(output, path)
	case "python":
		return parsePython(output, path)
	default:
		return parseGeneric(output, path)
	}
}

func parseGeneric(output, path string) []Row {
	var rows []Row
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := loc3Re.FindStringSubmatch(line); m != nil {
			if m[1] != path {
				continue
			}
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			rows = append(rows, Row{Severity: SeverityError, Line: ln, Col: col, Source: "fallback", Message: m[4]})
			continue
		}
		if m := loc2Re.FindStringSubmatch(line); m != nil {
			if m[1] != path {
				continue
			}
			ln, _ := strconv.Atoi(m[2])
			rows = append(rows, Row{Severity: SeverityError, Line: ln, Source: "fallback", Message: m[3]})
		}
	}
	return rows
}

func parseBash(output, path string) []Row {
	var rows []Row
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := bashRe.FindStringSubmatch(line); m != nil {
			if !strings.EqualFold(m[1], filepath.Base(path)) && m[1] != path {
				continue
			}
			ln, _ := strconv.Atoi(m[2])
			rows = append(rows, Row{Severity: SeverityError, Line: ln, Source: "fallback", Message: m[3]})
		}
	}
	return rows
}

func parsePython(output, path string) []Row {
	var rows []Row
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := pyRe.FindStringSubmatch(line); m != nil {
			if m[3] != path && m[3] != filepath.Base(path) {
				continue
			}
			ln, _ := strconv.Atoi(m[4])
			msg := fmt.Sprintf("%s: %s", m[1], m[2])
			rows = append(rows, Row{Severity: SeverityError, Line: ln, Source: "fallback", Message: msg})
		}
	}
	return rows
}
