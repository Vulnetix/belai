package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/jsonrpc"
	"github.com/vulnetix/signet/internal/proc"
	"github.com/vulnetix/signet/internal/sandbox"
)

// stdioTransport runs a server as a child process speaking newline-delimited
// JSON-RPC on stdin and stdout.
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	conn   *jsonrpc.Conn
	stderr *tail
	exited chan struct{}
	cancel context.CancelFunc
}

// startStdio starts command with the scrubbed environment plus env, in its
// own process group, optionally under the OS sandbox.
func startStdio(command string, args []string, env map[string]string, dir string, policy *sandbox.Policy) (*stdioTransport, error) {
	// The context lives as long as the server; cancelling it kills the
	// process group (proc.SetProcessGroup installs that Cancel).
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = proc.ScrubbedEnv()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+expand(env[k]))
	}
	proc.SetProcessGroup(cmd)
	if policy != nil {
		if _, err := sandbox.Wrap(cmd, *policy); err != nil {
			cancel()
			return nil, err
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	st := &tail{max: 4096}
	cmd.Stderr = st
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, stderr: st, exited: make(chan struct{}), cancel: cancel}
	t.conn = jsonrpc.NewConn(stdout, stdin, serverRequests)
	go func() {
		_ = cmd.Wait()
		close(t.exited)
	}()
	return t, nil
}

// serverRequests answers requests a server sends the client. Signet offers
// no roots, sampling or elicitation, so only ping is answered.
func serverRequests(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "ping" {
		return map[string]any{}, nil
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "signet does not support %s", method)
}

func (t *stdioTransport) call(ctx context.Context, method string, params, result any) error {
	return t.conn.Call(ctx, method, params, result)
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	return t.conn.Notify(method, params)
}

func (t *stdioTransport) close() error {
	t.conn.Close()
	// Closing stdin is the polite stop; a server still running two seconds
	// later is killed with its whole process group.
	_ = t.stdin.Close()
	go func() {
		select {
		case <-t.exited:
		case <-time.After(2 * time.Second):
		}
		t.cancel()
	}()
	return nil
}

func (t *stdioTransport) diag() string { return t.stderr.String() }

// tail keeps the last max bytes written.
type tail struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
