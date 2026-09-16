package tools

import "time"

// Default builds the default tool registry for a working directory: Read
// (bounded to 64 KiB), Write, Edit, WebFetch, WebSearch when a search backend
// is configured, Grep, Glob, and Bash. readOnly is the master read-only
// switch: when true, every mutating tool (Write, Edit, and full Bash) is
// removed from the registry — a read-only Bash still ships, so inspection
// remains available.
func Default(workdir string, readOnly bool) *Registry {
	var list []Tool
	list = append(list, &Read{Root: workdir, MaxBytes: 64 * 1024})
	list = append(list, &Write{Root: workdir, MaxBytes: MaxWriteBytes})
	list = append(list, &Edit{Root: workdir, MaxBytes: MaxWriteBytes})
	list = append(list, &WebFetch{})
	ws := &WebSearch{}
	if ws.Available() {
		list = append(list, ws)
	}
	list = append(list, &Bash{Root: workdir, ReadOnly: readOnly, Timeout: 30 * time.Second, MaxBytes: 64 * 1024})
	list = append(list, &Grep{Root: workdir, MaxMatches: 200, MaxLineLen: 200})
	list = append(list, &Glob{Root: workdir, MaxResults: 200})

	base := NewRegistry(list...)
	if readOnly {
		return base.ReadOnly()
	}
	return base
}
