package agentstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentStatus is the probed state of one registry agent.
type AgentStatus struct {
	Name string
	// Present is true when the agent has at least one reachable transcript or
	// prompt-index file (and, for SQLite agents, sqlite3 is available).
	Present bool
	// Reason explains why a non-present agent is skipped ("sqlite3 not on
	// PATH", "no store files", or "empty").
	Reason   string
	Sessions []string // concrete transcript/store paths
	Prompts  []string // concrete prompt-index paths
	Memory   []string // concrete memory/rules paths
}

// probeOnce runs the probe lazily, exactly once.
func (r *Registry) probeOnce() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probed {
		return
	}
	r.probed = true
	r.sqlite, _ = exec.LookPath("sqlite3")
	r.status = r.probeLocked()
}

// probeLocked stats the registry table against the filesystem.
func (r *Registry) probeLocked() []AgentStatus {
	home := r.home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	out := make([]AgentStatus, 0, len(knownAgents))
	for _, a := range knownAgents {
		st := AgentStatus{Name: a.Name}
		sessions, err := expandGlobs(a.Sessions, home, r.workdir)
		if err == nil {
			st.Sessions = sessions
		}
		prompts, err := expandGlobs(a.Prompts, home, r.workdir)
		if err == nil {
			st.Prompts = prompts
		}
		memory, err := expandGlobs(a.Memory, home, r.workdir)
		if err == nil {
			st.Memory = memory
		}

		switch a.Format {
		case FormatSQLiteGoose, FormatSQLiteOpenCode:
			if r.sqlite == "" {
				st.Reason = "sqlite3 not on PATH"
			} else if len(sessions) == 0 {
				st.Reason = "no store files"
			} else {
				st.Present = true
			}
		default:
			if len(sessions) > 0 || len(prompts) > 0 {
				st.Present = true
			} else if anyStoreDirExists(a, home, r.workdir) {
				st.Reason = "empty"
			} else {
				st.Reason = "no store files"
			}
		}
		out = append(out, st)
	}
	return out
}

// anyStoreDirExists reports whether any session/prompt glob's static prefix
// directory exists, distinguishing "empty" from "no store files".
func anyStoreDirExists(a Agent, home, workdir string) bool {
	for _, p := range append(append([]string{}, a.Sessions...), a.Prompts...) {
		if dir, ok := staticDir(p, home, workdir); ok {
			if _, err := os.Stat(dir); err == nil {
				return true
			}
		}
	}
	return false
}

// staticDir returns the literal directory before the first glob metacharacter.
func staticDir(pattern, home, workdir string) (string, bool) {
	raw := pattern
	switch {
	case raw == "~":
		raw = home
	case strings.HasPrefix(raw, "~/"):
		raw = filepath.Join(home, raw[2:])
	case !filepath.IsAbs(raw):
		raw = filepath.Join(workdir, raw)
	}
	raw = filepath.Clean(raw)
	segs := strings.Split(filepath.ToSlash(raw), "/")
	for i, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			if i == 0 {
				return "", false
			}
			dir := strings.Join(segs[:i], "/")
			if filepath.IsAbs(raw) {
				dir = "/" + dir
			}
			return dir, true
		}
	}
	return raw, true
}

// Status returns every agent's probed status in registry order.
func (r *Registry) Status() []AgentStatus {
	r.probeOnce()
	return r.status
}

// statusFor returns the probed status for one agent, or nil when unknown.
func (r *Registry) statusFor(name string) *AgentStatus {
	for i := range r.Status() {
		if r.status[i].Name == name {
			return &r.status[i]
		}
	}
	return nil
}
