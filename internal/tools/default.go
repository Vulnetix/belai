package tools

import (
	"time"

	"github.com/vulnetix/signet/internal/agentstore"
)

// Default builds the default tool registry for a working directory: Read
// (bounded to 64 KiB), Write, Edit, WebFetch, WebSearch when a search backend
// is configured, Bash, Grep, Glob, and Cd. readOnly is the master read-only
// switch: when true, every mutating tool (Write, Edit, and full Bash) is
// removed from the registry — a read-only Bash still ships, so inspection
// remains available.
//
// Every path-taking tool shares one working-directory tracker rooted at
// workdir, so a Cd call moves all of them together and none of them can be
// left pointing at a directory the others have left.
func Default(workdir string, readOnly bool) *Registry {
	cwd := NewCwd(workdir)
	var list []Tool
	list = append(list, &Read{Root: workdir, MaxBytes: 64 * 1024, Cwd: cwd})
	list = append(list, &Write{Root: workdir, MaxBytes: MaxWriteBytes, Cwd: cwd})
	list = append(list, &Edit{Root: workdir, MaxBytes: MaxWriteBytes, Cwd: cwd})
	list = append(list, &WebFetch{})
	ws := &WebSearch{}
	if ws.Available() {
		list = append(list, ws)
	}
	list = append(list, &Bash{Root: workdir, ReadOnly: readOnly, Timeout: 30 * time.Second, MaxBytes: 64 * 1024, Cwd: cwd})
	list = append(list, &Grep{Root: workdir, MaxMatches: 200, MaxLineLen: 200, Cwd: cwd})
	list = append(list, &Glob{Root: workdir, MaxResults: 200, Cwd: cwd})
	list = append(list, &Cd{Cwd: cwd})
	list = append(list, UpdatePlan{})
	list = append(list, ExitPlanMode{})
	// The three agent-store tools read other agents' stores through the
	// static registry. Probing is lazy (first use), not at startup, so a
	// registry built here costs nothing until a call is made.
	list = append(list, NewAgentStoreTools(agentstore.New("", workdir), workdir)...)

	base := NewRegistry(list...)
	base.cwd = cwd
	if readOnly {
		return base.ReadOnly()
	}
	return base
}
