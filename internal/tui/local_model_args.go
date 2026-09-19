package tui

import "strings"

// localModelFlags captures the parsed arguments of a /local-model invocation.
type localModelFlags struct {
	repo  string
	port  string
	quant string
}

// parseLocalModelArgs extracts the subcommand, repo, and flags from a
// /local-model command line. It is intentionally permissive: unknown flags
// are ignored so the UI can report usage rather than silently failing.
func parseLocalModelArgs(arg string) (string, localModelFlags) {
	fields := strings.Fields(arg)
	var sub string
	var f localModelFlags
	if len(fields) > 0 && !strings.HasPrefix(fields[0], "--") {
		sub = fields[0]
		fields = fields[1:]
	}
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "--port":
			if i+1 < len(fields) {
				f.port = fields[i+1]
				i++
			}
		case "--quant":
			if i+1 < len(fields) {
				f.quant = fields[i+1]
				i++
			}
		default:
			if f.repo == "" && !strings.HasPrefix(fields[i], "--") {
				f.repo = fields[i]
			}
		}
	}
	return sub, f
}
