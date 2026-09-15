package tools

import "time"

// Default builds the default tool registry for a working directory: Read
// (bounded to 64 KiB), WebFetch, WebSearch when a search backend is
// configured, and Bash (read-only by default, gated by permissions and
// plan-mode allowlists).
func Default(workdir string) *Registry {
	var list []Tool
	list = append(list, &Read{Root: workdir, MaxBytes: 64 * 1024})
	list = append(list, &WebFetch{})
	ws := &WebSearch{}
	if ws.Available() {
		list = append(list, ws)
	}
	list = append(list, &Bash{Root: workdir, Timeout: 30 * time.Second, MaxBytes: 64 * 1024})
	list = append(list, &Grep{Root: workdir, MaxMatches: 200, MaxLineLen: 200})
	list = append(list, &Glob{Root: workdir, MaxResults: 200})
	return NewRegistry(list...)
}
