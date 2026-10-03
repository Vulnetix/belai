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

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/proc"
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
	// Env is extra NAME=value entries the harness composes (never a model's
	// text), appended after the scrubbed environment.
	Env      []string
	Registry *activity.Registry
	Label    string // activity label; empty uses the binary name
	Pidfile  string
	OnLine   func(string) // optional sink for stdout/stderr lines
	// OnPort, when set, is told the port a bind retry moved the server to,
	// so the caller can persist the address it actually answers on.
	OnPort func(port int)
}

// LaunchError is a launch that failed with something to say about why: the
// server exited early (ExitCode >= 0) or never became healthy (ExitCode -1).
// Class is ClassifyLog of the captured output, so a caller can offer a fix
// without parsing the text itself.
type LaunchError struct {
	ExitCode int
	Output   string
	Class    LogClass
	Msg      string
}

func (e *LaunchError) Error() string {
	if e.Output == "" {
		return e.Msg
	}
	return e.Msg + "\n" + e.Output
}

// Launch starts the server and waits for it to report ready. It uses process
// groups, runs the server with the scrubbed environment, tees its output,
// registers the process in the activity registry, and returns a stop function
// that SIGTERMs the group then SIGKILLs after a short grace period.
//
// ctx bounds the startup only: cancelling it before the server is healthy
// stops the server, but a server that became healthy outlives ctx and runs
// until stop is called. A server that exits during startup is reported at
// once as a *LaunchError rather than after the whole deadline.
//
// If the server fails to bind because its port is already in use, Launch
// retries up to three times with a freshly allocated port. This covers the
// advisory nature of FreePort: a collision between closing the probe socket
// and the server binding it is rare but possible.
func Launch(ctx context.Context, bin Binary, args []string, baseURL string, opts LaunchOptions) (stop func() error, err error) {
	if bin.Path == "" {
		return nil, errors.New("no server binary")
	}
	if opts.Deadline <= 0 {
		opts.Deadline = 30 * time.Second
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			port, err := FreePort()
			if err != nil {
				return nil, fmt.Errorf("free port for retry: %w", err)
			}
			args = replacePortInArgs(args, port)
			baseURL = BaseURL("127.0.0.1", port)
			if opts.Pidfile != "" {
				_ = os.Remove(opts.Pidfile)
			}
			if opts.OnPort != nil {
				opts.OnPort(port)
			}
		}

		stop, healthy, addrInUse, err := tryLaunch(ctx, bin, args, baseURL, opts)
		if healthy {
			return stop, nil
		}
		if stop != nil {
			_ = stop()
		}
		if !addrInUse {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("local server failed to bind after retries")
}

func tryLaunch(ctx context.Context, bin Binary, args []string, baseURL string, opts LaunchOptions) (stop func() error, healthy, addrInUse bool, err error) {
	// exec.Command, not CommandContext: the context bounds the startup only.
	// A context-bound command would be killed (with its whole process group)
	// the moment the caller's startup context is cancelled, which for a
	// successful launch is right after this function returns.
	cmd := exec.Command(bin.Path, args...)
	proc.SetProcessGroup(cmd)
	cmd.Cancel = nil

	label := opts.Label
	if label == "" {
		label = bin.Name
	}
	var handle *activity.Handle
	if opts.Registry != nil {
		handle = opts.Registry.Add(activity.Activity{
			Kind:  activity.KindShell,
			Label: label,
			Argv:  append([]string{bin.Path}, args...),
			State: activity.StateRunning,
		}, func() {
			if stop != nil {
				_ = stop()
			}
		})
	}

	// The server never sees provider keys or Belai configuration. The
	// Hugging Face token is added back only when the caller passes one (the
	// -hf download path); a -m launch needs none.
	cmd.Env = proc.ScrubbedEnv()
	if opts.HFToken != "" {
		cmd.Env = append(cmd.Env, "HF_TOKEN="+opts.HFToken)
	}
	cmd.Env = append(cmd.Env, opts.Env...)

	tee := proc.NewLineTee(0, opts.OnLine)
	cmd.Stdout = tee
	cmd.Stderr = tee

	if err := cmd.Start(); err != nil {
		if handle != nil {
			handle.Finish(0, false, err)
		}
		return nil, false, isAddrInUse(err.Error()), fmt.Errorf("start %s: %w", bin.Name, err)
	}

	pid := cmd.Process.Pid
	if opts.Pidfile != "" {
		_ = os.WriteFile(opts.Pidfile, []byte(strconv.Itoa(pid)), 0o600)
	}

	// One goroutine owns Wait for the life of the process, so an early exit
	// is seen during startup and stop never waits twice.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	stop = makeStop(cmd, exited, handle, opts.Pidfile)

	deadline := time.NewTimer(opts.Deadline)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		if serverReady(ctx, baseURL) {
			return stop, true, false, nil
		}
		select {
		case <-exited:
			tee.Flush()
			output := tee.Content()
			code := -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			return stop, false, isAddrInUse(output), &LaunchError{
				ExitCode: code, Output: output, Class: ClassifyLog(output),
				Msg: fmt.Sprintf("%s exited during startup (exit code %d)", bin.Name, code),
			}
		case <-ctx.Done():
			return stop, false, false, ctx.Err()
		case <-deadline.C:
			tee.Flush()
			output := tee.Content()
			return stop, false, isAddrInUse(output), &LaunchError{
				ExitCode: -1, Output: output, Class: ClassifyLog(output),
				Msg: fmt.Sprintf("local server did not become healthy at %s within %s", baseURL, opts.Deadline),
			}
		case <-tick.C:
			tee.Flush()
			if output := tee.Content(); isAddrInUse(output) {
				return stop, false, true, fmt.Errorf("%s could not bind: %s", bin.Name, output)
			}
		}
	}
}

// serverReady reports whether the server at baseURL is ready to answer.
// llama-server answers /health with 503 while the model is still loading and
// 200 once it can serve, so a 503 there is "not yet" even if /v1/models
// already answers. Servers without /health fall back to the /v1/models probe.
func serverReady(ctx context.Context, baseURL string) bool {
	root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/health", nil)
	if err == nil {
		if resp, err := httpclient.Default().Do(req); err == nil {
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				return true
			case http.StatusServiceUnavailable:
				return false
			}
		}
	}
	return ProbeRunning(ctx, []string{baseURL}) == baseURL
}

// replacePortInArgs returns a copy of args with the value following --port
// replaced by port. If no --port flag exists, args is returned unchanged.
func replacePortInArgs(args []string, port int) []string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--port" {
			out[i+1] = strconv.Itoa(port)
			return out
		}
	}
	return out
}

// isAddrInUse is a best-effort heuristic for the bind failure that should
// trigger a port retry.
func isAddrInUse(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "address already in use") ||
		strings.Contains(lower, "bind failed") ||
		strings.Contains(lower, "eaddrinuse")
}
