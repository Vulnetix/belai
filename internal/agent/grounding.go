package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/sanitize"
)

// Grounding is the always-useful, read-only workspace evidence attached to
// every explore subagent. It is untrusted: it re-enters the conversation as
// part of the subagent prompt and is admitted through the Role Manager before
// the subagent's own model turn, never promoted into a system/agent block.
type Grounding struct {
	GitStatus     string
	Branch        string
	RecentCommits string
	Layout        string
	AgentsMD      string
	AgentNames    []string
}

const (
	groundingTimeout  = 3 * time.Second
	groundingMaxBytes = 16 * 1024
	groundingMaxList  = 200
)

// groundingProbe runs the short, non-mutating grounding probe: git state, a
// bounded top-level directory listing, AGENTS.md, and the relevant background
// agents. Every step is read-only, capped, and timed out; a failure is silent
// (the field stays empty) rather than aborting exploration.
func (s *Session) groundingProbe(ctx context.Context) Grounding {
	g := Grounding{
		GitStatus:     runProbe(ctx, s.workdir, "git", "status", "--short"),
		Branch:        runProbe(ctx, s.workdir, "git", "rev-parse", "--abbrev-ref", "HEAD"),
		RecentCommits: runProbe(ctx, s.workdir, "git", "log", "--oneline", "-5"),
		Layout:        listLayout(s.workdir),
		AgentsMD:      readAgentsMD(s.workdir),
		AgentNames:    relevantAgents(),
	}
	return g
}

// digest renders the grounding evidence as one sanitized text block for the
// subagent prompt. It is model input, so it is sanitized exactly like any
// other user-supplied content.
func (g Grounding) digest() string {
	var b strings.Builder
	if g.Branch != "" || g.GitStatus != "" {
		b.WriteString("Repository state:\n")
		if g.Branch != "" {
			b.WriteString("branch: " + g.Branch + "\n")
		}
		if g.GitStatus != "" {
			b.WriteString("git status --short:\n" + g.GitStatus + "\n")
		}
		if g.RecentCommits != "" {
			b.WriteString("recent commits:\n" + g.RecentCommits + "\n")
		}
	}
	if g.Layout != "" {
		b.WriteString("Top-level layout:\n" + g.Layout + "\n")
	}
	if g.AgentsMD != "" {
		b.WriteString("AGENTS.md:\n" + g.AgentsMD + "\n")
	}
	if len(g.AgentNames) > 0 {
		b.WriteString("Available background agents:\n" + strings.Join(g.AgentNames, ", ") + "\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return sanitize.Sanitize(b.String())
}

// runProbe runs one read-only grounding command with a timeout and output cap.
// An absent binary or a non-zero exit yields "".
func runProbe(ctx context.Context, dir, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, groundingTimeout)
	defer cancel()
	ec := exec.CommandContext(pctx, name, args...)
	ec.Dir = dir
	ec.Env = scrubbedEnvForProbe()
	out, err := ec.Output()
	if err != nil {
		return ""
	}
	return capProbe(out)
}

func capProbe(out []byte) string {
	if len(out) == 0 {
		return ""
	}
	if len(out) > groundingMaxBytes {
		out = out[:groundingMaxBytes]
	}
	return strings.TrimRight(string(out), "\n")
}

// scrubbedEnvForProbe reuses the tools scrubber so probe output can never
// exfiltrate a credential. It keeps the bare environment minus secrets.
func scrubbedEnvForProbe() []string {
	var out []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
			strings.HasPrefix(upper, "CLOUDFLARE_") || strings.HasPrefix(upper, "SIGNET_") ||
			strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// listLayout returns a bounded top-level directory listing, one entry per
// line with directories marked.
func listLayout(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > groundingMaxList {
		names = names[:groundingMaxList]
	}
	return strings.Join(names, "\n")
}

// readAgentsMD returns AGENTS.md content, capped. Missing file is silent.
func readAgentsMD(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return ""
	}
	if len(b) > groundingMaxBytes {
		b = b[:groundingMaxBytes]
	}
	return strings.TrimRight(string(b), "\n")
}

// relevantAgents lists the user's background agents that are not scheduled,
// loop, or monitor definitions — i.e. the flat, single-shot profiles an
// explore subagent could plausibly consult. Scheduled/loop/monitor definitions
// are background processes, not evidence to attach.
func relevantAgents() []string {
	profiles, err := agentprofile.List()
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range profiles {
		if p.Mode == agentprofile.ModeSingle {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names
}
