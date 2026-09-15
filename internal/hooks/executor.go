package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner executes a validated hook command under a hooks root directory.
// Commands run argv-direct (never through a shell), with a scrubbed
// environment and bounded timeout/output, after re-resolving the command path
// through the root so a symlink inside the hooks directory cannot point at
// /bin/sh.
type Runner struct {
	Root     string
	Timeout  time.Duration
	MaxBytes int
}

// Run executes h.Command and returns its combined output. A nonzero exit is
// reported as an error so callers can fail closed on pre- hooks.
func (r *Runner) Run(ctx context.Context, h Hook) (string, error) {
	fields := strings.Fields(h.Command)
	if len(fields) == 0 {
		return "", fmt.Errorf("hook %q has an empty command", h.Name)
	}
	resolved, err := r.resolve(fields[0])
	if err != nil {
		return "", err
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	ec := exec.CommandContext(ctx, resolved, fields[1:]...)
	ec.Dir = r.Root
	ec.Env = scrubbedEnv()
	out, err := ec.CombinedOutput()
	content := string(out)
	if r.MaxBytes > 0 && len(content) > r.MaxBytes {
		content = content[:r.MaxBytes] + "\n… truncated"
	}
	if err != nil {
		return content, fmt.Errorf("hook %q: %w", h.Name, err)
	}
	return content, nil
}

// resolve maps a relative command path onto the hooks root and confirms it
// still resolves inside the root after following symlinks.
func (r *Runner) resolve(cmd string) (string, error) {
	absRoot, err := filepath.Abs(r.Root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, cmd)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("hook command %q escapes the hooks root", cmd)
	}
	return resolved, nil
}

func scrubbedEnv() []string {
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
