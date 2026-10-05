// Package codemode runs a model-written script in a confined JavaScript
// interpreter and bridges it to the session's tools.
//
// The interpreter is goja, a pure-Go engine, so the release stays
// CGO_ENABLED=0. It has no filesystem, process, network, module or timer
// globals: the only way out is the host functions Run injects, and every one
// of them calls back into the session's own gate pipeline (permissions, ask
// gate, hooks, sandbox, sanitising and the classifier). The script therefore
// only ever sees text the harness already admitted, or the withheld
// placeholder, and what it prints is composed from that text and the model's
// own literals.
//
// goja has no hard heap limit. The mitigations are a wall-clock timeout, a
// nested-call budget, an output cap, size guards on the string builders a
// script could use to allocate in one step, and a sampled heap watchdog that
// interrupts a script whose growth passes Config.MaxHeap.
package codemode

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// Defaults for Config. A project layer may only lower them.
const (
	DefaultTimeout   = 120 * time.Second
	DefaultMaxCalls  = 50
	DefaultMaxOutput = 16 * 1024
	DefaultMaxScript = 32 * 1024
	DefaultMaxHeap   = 256 << 20

	// hardOutput stops a script that prints without end, whatever MaxOutput
	// the caller set.
	hardOutput = 1 << 20
	// maxBuilt bounds one string a script builds with repeat/padStart/padEnd.
	maxBuilt = 4 << 20
)

// Config bounds one run. Zero fields take the defaults.
type Config struct {
	Timeout   time.Duration
	MaxCalls  int
	MaxOutput int
	MaxScript int
	MaxHeap   uint64
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxCalls <= 0 {
		c.MaxCalls = DefaultMaxCalls
	}
	if c.MaxOutput <= 0 {
		c.MaxOutput = DefaultMaxOutput
	}
	if c.MaxScript <= 0 {
		c.MaxScript = DefaultMaxScript
	}
	if c.MaxHeap == 0 {
		c.MaxHeap = DefaultMaxHeap
	}
	return c
}

// Env is everything a script can reach. All of it is supplied by the host.
type Env struct {
	// Call runs one nested tool call through the session's gate pipeline and
	// returns the admitted text.
	Call func(ctx context.Context, name string, args map[string]any) string
	// Tools are the names callable as tools.<Name>(args).
	Tools []string
	// ReadOnly reports whether a tool may run concurrently in tools.parallel.
	ReadOnly func(name string) bool
	// Search and Describe back tools.search and tools.describe. Both return
	// names or schema structure only.
	Search   func(query string) []string
	Describe func(name string) string
	// MCP maps a server name to its tool names. MCPName gives the full tool
	// name (mcp__server__tool) Call is invoked with, and MCPDescribe the
	// already-admitted description text for mcp.describe.
	MCP         map[string][]string
	MCPName     func(server, tool string) string
	MCPDescribe func(ctx context.Context, server, tool string) string
}

// Result is the outcome of one run.
type Result struct {
	// Output is what the script printed plus its returned value.
	Output string
	// Calls is the number of nested calls made.
	Calls int
	// Truncated reports that Output was cut at the cap.
	Truncated bool
	// Err describes a script error, a timeout or a limit, in harness words.
	Err error
}

// ErrScriptTooLarge is returned for a script over Config.MaxScript.
var ErrScriptTooLarge = errors.New("script is too large")

type runner struct {
	cfg   Config
	env   Env
	vm    *goja.Runtime
	ctx   context.Context
	calls atomic.Int64

	mu        sync.Mutex
	out       strings.Builder
	truncated bool
	reason    atomic.Value // string: why the run was interrupted
}

// Run executes script and returns what it printed. It never panics.
func Run(ctx context.Context, script string, env Env, cfg Config) (res Result) {
	cfg = cfg.withDefaults()
	if len(script) > cfg.MaxScript {
		return Result{Err: fmt.Errorf("%w: %d bytes, limit %d", ErrScriptTooLarge, len(script), cfg.MaxScript)}
	}
	if strings.TrimSpace(script) == "" {
		return Result{Err: errors.New("script is empty")}
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	r := &runner{cfg: cfg, env: env, vm: goja.New(), ctx: ctx}
	defer func() {
		if p := recover(); p != nil {
			res = Result{Output: r.output(), Calls: int(r.calls.Load()), Err: fmt.Errorf("script runtime failed: %v", p)}
		}
	}()
	if err := r.install(); err != nil {
		return Result{Err: err}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go r.watch(stop, &wg)

	// The wrapper keeps the script on the first line so line numbers in an
	// error match the model's own, and gives `return` a meaning.
	v, err := r.vm.RunString("(function(){" + script + "\n})()")
	close(stop)
	wg.Wait()

	res = Result{Calls: int(r.calls.Load())}
	if err == nil && v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		r.print(r.format(v))
	}
	res.Output = r.output()
	res.Truncated = r.truncatedOut()
	if err != nil {
		res.Err = r.describeErr(err)
	}
	return res
}

func (r *runner) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

func (r *runner) truncatedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.truncated
}

func (r *runner) interrupt(why string) {
	r.reason.CompareAndSwap(nil, why)
	r.vm.Interrupt(why)
}

// watch interrupts the interpreter on timeout, cancellation or heap growth.
func (r *runner) watch(stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-r.ctx.Done():
			if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
				r.interrupt(fmt.Sprintf("script timed out after %s", r.cfg.Timeout))
			} else {
				r.interrupt("script cancelled")
			}
			return
		case <-tick.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > base.HeapAlloc && m.HeapAlloc-base.HeapAlloc > r.cfg.MaxHeap {
				r.interrupt(fmt.Sprintf("script used more than %d MiB of memory", r.cfg.MaxHeap>>20))
				return
			}
		}
	}
}

func (r *runner) describeErr(err error) error {
	if why, _ := r.reason.Load().(string); why != "" {
		return errors.New(why)
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return fmt.Errorf("script error: %s", firstLines(ex.String(), 6))
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return errors.New("script interrupted")
	}
	return fmt.Errorf("script error: %v", err)
}

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// print appends to the output, cutting at the cap.
func (r *runner) print(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.truncated {
		return
	}
	if r.out.Len() > 0 {
		s = "\n" + s
	}
	room := r.cfg.MaxOutput
	if room > hardOutput {
		room = hardOutput
	}
	if r.out.Len()+len(s) > room {
		keep := room - r.out.Len()
		if keep > 0 {
			r.out.WriteString(cutUTF8(s, keep))
		}
		r.truncated = true
		return
	}
	r.out.WriteString(s)
}

func cutUTF8(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// format renders a returned or printed value: strings as-is, objects as JSON.
func (r *runner) format(v goja.Value) string {
	if s, ok := v.Export().(string); ok {
		return s
	}
	obj, ok := v.(*goja.Object)
	if !ok {
		return v.String()
	}
	if js, ok := goja.AssertFunction(r.vm.Get("JSON").ToObject(r.vm).Get("stringify")); ok {
		if out, err := js(goja.Undefined(), obj, goja.Null(), r.vm.ToValue(2)); err == nil {
			return out.String()
		}
	}
	return v.String()
}

func (r *runner) fail(msg string) {
	panic(r.vm.NewGoError(errors.New(msg)))
}

// callTool is the one place a script reaches the host.
func (r *runner) callTool(name string, args map[string]any) string {
	if r.calls.Add(1) > int64(r.cfg.MaxCalls) {
		r.fail(fmt.Sprintf("tool call budget of %d exhausted; do more per call or print what you have", r.cfg.MaxCalls))
	}
	if err := r.ctx.Err(); err != nil {
		r.fail("script stopped: " + err.Error())
	}
	if args == nil {
		args = map[string]any{}
	}
	return r.env.Call(r.ctx, name, args)
}

func (r *runner) argsOf(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}
	}
	m, ok := v.Export().(map[string]any)
	if !ok {
		r.fail("tool arguments must be an object, e.g. tools.Read({file_path: \"x\"})")
	}
	return m
}

func (r *runner) install() error {
	vm := r.vm
	// Size guards for one-step allocation helpers.
	if _, err := vm.RunString(`(function(){
  var cap = ` + fmt.Sprint(maxBuilt) + `;
  function guard(proto, name, size) {
    var orig = proto[name];
    Object.defineProperty(proto, name, {value: function() {
      var n = size(this, arguments);
      if (n > cap) { throw new RangeError(name + " would build a string of " + n + " characters; limit " + cap); }
      return orig.apply(this, arguments);
    }, writable: false, configurable: false});
  }
  guard(String.prototype, "repeat", function(s, a){ return String(s).length * Math.max(0, Number(a[0]) || 0); });
  guard(String.prototype, "padStart", function(s, a){ return Math.max(String(s).length, Number(a[0]) || 0); });
  guard(String.prototype, "padEnd", function(s, a){ return Math.max(String(s).length, Number(a[0]) || 0); });
})()`); err != nil {
		return err
	}

	printFn := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, r.format(a))
		}
		r.print(strings.Join(parts, " "))
		if r.truncatedOut() {
			r.interrupt(fmt.Sprintf("script printed more than %d bytes; print less or filter first", r.cfg.MaxOutput))
		}
		return goja.Undefined()
	}
	_ = vm.Set("print", printFn)
	console := vm.NewObject()
	for _, n := range []string{"log", "info", "warn", "error"} {
		_ = console.Set(n, printFn)
	}
	_ = vm.Set("console", console)

	tools := vm.NewObject()
	names := append([]string(nil), r.env.Tools...)
	sort.Strings(names)
	for _, n := range names {
		name := n
		fn := func(call goja.FunctionCall) goja.Value {
			return vm.ToValue(r.callTool(name, r.argsOf(call.Argument(0))))
		}
		_ = tools.Set(name, fn)
		if lo := strings.ToLower(name); lo != name {
			_ = tools.Set(lo, fn)
		}
	}
	_ = tools.Set("parallel", func(call goja.FunctionCall) goja.Value { return r.parallel(call.Argument(0)) })
	_ = tools.Set("list", func(goja.FunctionCall) goja.Value { return vm.ToValue(names) })
	_ = tools.Set("search", func(call goja.FunctionCall) goja.Value {
		if r.env.Search == nil {
			return vm.ToValue([]string{})
		}
		return vm.ToValue(r.env.Search(call.Argument(0).String()))
	})
	_ = tools.Set("describe", func(call goja.FunctionCall) goja.Value {
		if r.env.Describe == nil {
			return vm.ToValue("")
		}
		return vm.ToValue(r.env.Describe(call.Argument(0).String()))
	})
	_ = vm.Set("tools", tools)

	mcp := vm.NewObject()
	servers := make([]string, 0, len(r.env.MCP))
	for s := range r.env.MCP {
		servers = append(servers, s)
	}
	sort.Strings(servers)
	for _, s := range servers {
		server := s
		obj := vm.NewObject()
		for _, t := range r.env.MCP[server] {
			tool := t
			_ = obj.Set(tool, func(call goja.FunctionCall) goja.Value {
				return vm.ToValue(r.callTool(r.env.MCPName(server, tool), r.argsOf(call.Argument(0))))
			})
		}
		_ = mcp.Set(server, obj)
	}
	_ = mcp.Set("list", func(goja.FunctionCall) goja.Value {
		out := map[string]any{}
		for _, s := range servers {
			out[s] = r.env.MCP[s]
		}
		return vm.ToValue(out)
	})
	_ = mcp.Set("describe", func(call goja.FunctionCall) goja.Value {
		server, tool, ok := strings.Cut(call.Argument(0).String(), ".")
		if !ok || r.env.MCPDescribe == nil {
			r.fail(`mcp.describe takes "server.tool"`)
		}
		if !contains(r.env.MCP[server], tool) {
			r.fail(fmt.Sprintf("no MCP tool %q", server+"."+tool))
		}
		return vm.ToValue(r.env.MCPDescribe(r.ctx, server, tool))
	})
	_ = vm.Set("mcp", mcp)

	// Lock the bridges so a script cannot replace them.
	_, err := vm.RunString(`Object.freeze(tools); Object.freeze(mcp); Object.freeze(console);`)
	return err
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// parallel runs [{tool, args}] or [[tool, args]] and returns the results in
// order. Read-only tools run concurrently; the rest run afterwards, one at a
// time in the order written, so nothing that mutates ever overlaps.
func (r *runner) parallel(v goja.Value) goja.Value {
	list, ok := v.Export().([]any)
	if !ok {
		r.fail("tools.parallel takes an array of {tool, args}")
	}
	type job struct {
		name string
		args map[string]any
	}
	jobs := make([]job, len(list))
	for i, it := range list {
		switch x := it.(type) {
		case map[string]any:
			name, _ := x["tool"].(string)
			args, _ := x["args"].(map[string]any)
			jobs[i] = job{name, args}
		case []any:
			if len(x) > 0 {
				name, _ := x[0].(string)
				var args map[string]any
				if len(x) > 1 {
					args, _ = x[1].(map[string]any)
				}
				jobs[i] = job{name, args}
			}
		}
		if jobs[i].name == "" || !contains(r.env.Tools, jobs[i].name) {
			r.fail(fmt.Sprintf("tools.parallel item %d does not name a tool", i))
		}
	}
	if r.calls.Add(int64(len(jobs))-1) >= int64(r.cfg.MaxCalls) {
		r.fail(fmt.Sprintf("tool call budget of %d exhausted", r.cfg.MaxCalls))
	}
	out := make([]string, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, j := range jobs {
		if r.env.ReadOnly != nil && r.env.ReadOnly(j.name) {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					if p := recover(); p != nil {
						out[i] = fmt.Sprintf("tool call failed: %v", p)
					}
				}()
				a := j.args
				if a == nil {
					a = map[string]any{}
				}
				out[i] = r.env.Call(r.ctx, j.name, a)
			}()
		}
	}
	wg.Wait()
	for i, j := range jobs {
		if r.env.ReadOnly == nil || !r.env.ReadOnly(j.name) {
			a := j.args
			if a == nil {
				a = map[string]any{}
			}
			out[i] = r.env.Call(r.ctx, j.name, a)
		}
	}
	return r.vm.ToValue(out)
}
