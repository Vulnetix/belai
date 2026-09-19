package localinfer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/activity"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/proc"
)

// Binary is a launchable local inference server.
type Binary struct {
	Name string
	Path string
}

// Detect finds a launchable server binary. It follows the memoised LookPath
// pattern and reports the first of llama-server, ollama, or vllm found.
func Detect() (Binary, bool) {
	for _, name := range []string{"llama-server", "ollama", "vllm"} {
		if p, err := exec.LookPath(name); err == nil {
			return Binary{Name: name, Path: p}, true
		}
	}
	return Binary{}, false
}

// ArgsOptions controls how llama-server argv is built.
type ArgsOptions struct {
	Repo      string // HuggingFace repo id; used with Quant when ModelPath is empty
	Quant     string // quantisation suffix, e.g. Q4_K_M
	Port      int    // TCP port to bind
	ModelPath string // local GGUF path; wins over Repo/Quant
	HFRepo    string // alias for Repo; do not set both
	CtxSize   int
	NGL       int
}

// Args returns the launch args for llama-server. Exactly one of ModelPath or
// a HuggingFace repo may be supplied; ModelPath wins. Defaults keep today's
// sampling and performance settings: --jinja, loopback host, and the same
// sampling flags.
func Args(opts ArgsOptions) []string {
	port := opts.Port
	if port <= 0 {
		port = 8080
	}
	quant := opts.Quant
	if quant == "" {
		quant = "Q4_K_M"
	}
	repo := firstNonEmpty(opts.HFRepo, opts.Repo)

	ctx := opts.CtxSize
	if ctx <= 0 {
		ctx = 16384
	}
	ngl := opts.NGL
	if ngl <= 0 {
		ngl = 99
	}

	args := []string{
		"--jinja",
		"--temp", "1.0",
		"--top-p", "0.95",
		"--top-k", "64",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--no-mmap",
		"-fa", "on",
		"--n-gpu-layers", strconv.Itoa(ngl),
		"--ctx-size", strconv.Itoa(ctx),
	}

	if opts.ModelPath != "" {
		args = append([]string{"-m", opts.ModelPath}, args...)
	} else if repo != "" {
		args = append([]string{"-hf", repo + ":" + quant}, args...)
	}
	return args
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ProbeRunning returns the first base URL that answers GET /v1/models. It is
// how an already-running server is found. A base is accepted with or without
// its /v1 suffix — callers hold the OpenAI-surface base URL (".../v1"), and
// appending a second /v1 would probe a path no server serves. The returned
// value is the caller's base, spelled exactly as it was passed.
func ProbeRunning(ctx context.Context, bases []string) string {
	for _, base := range bases {
		url := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1") + "/v1/models"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := httpclient.Default().Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return base
		}
	}
	return ""
}

// LaunchOptions controls the supervision of a freshly launched server.
type LaunchOptions struct {
	Deadline time.Duration // health-check timeout; zero uses 30s
	HFToken  string        // passed to the child environment, never argv
	Registry *activity.Registry
	Pidfile  string
	OnLine   func(string) // optional sink for stdout/stderr lines
}

// Launch starts the server and waits for {baseURL}/v1/models to answer. It
// uses process groups, tees llama-server's output, registers the process in
// the activity registry, and returns a stop function that SIGTERMs the group
// then SIGKILLs after a short grace period.
func Launch(ctx context.Context, bin Binary, args []string, baseURL string, opts LaunchOptions) (stop func() error, err error) {
	if bin.Path == "" {
		return nil, errors.New("no server binary")
	}
	if opts.Deadline <= 0 {
		opts.Deadline = 30 * time.Second
	}

	cmd := exec.CommandContext(ctx, bin.Path, args...)
	proc.SetProcessGroup(cmd)

	var handle *activity.Handle
	if opts.Registry != nil {
		handle = opts.Registry.Add(activity.Activity{
			Kind:  activity.KindShell,
			Label: bin.Name,
			Argv:  append([]string{bin.Path}, args...),
			State: activity.StateRunning,
		}, func() {
			if stop != nil {
				_ = stop()
			}
		})
	}

	if opts.HFToken != "" {
		cmd.Env = append(os.Environ(), "HF_TOKEN="+opts.HFToken)
	}

	tee := proc.NewLineTee(0, opts.OnLine)
	cmd.Stdout = tee
	cmd.Stderr = tee

	if err := cmd.Start(); err != nil {
		if handle != nil {
			handle.Finish(0, false, err)
		}
		return nil, fmt.Errorf("start llama-server: %w", err)
	}

	pid := cmd.Process.Pid
	if opts.Pidfile != "" {
		_ = os.WriteFile(opts.Pidfile, []byte(strconv.Itoa(pid)), 0o600)
	}

	stop = makeStop(cmd, handle, opts.Pidfile)

	deadline := time.Now().Add(opts.Deadline)
	for time.Now().Before(deadline) {
		if ProbeRunning(ctx, []string{baseURL}) == baseURL {
			return stop, nil
		}
		select {
		case <-ctx.Done():
			_ = stop()
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	tee.Flush()
	_ = stop()
	return nil, fmt.Errorf("local server did not become healthy at %s\n%s", baseURL, tee.Content())
}
