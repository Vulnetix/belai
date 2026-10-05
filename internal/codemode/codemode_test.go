package codemode

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testEnv() Env {
	return Env{
		Tools:    []string{"Read", "Grep", "Edit"},
		ReadOnly: func(n string) bool { return n != "Edit" },
		Call: func(_ context.Context, name string, args map[string]any) string {
			if p, ok := args["path"].(string); ok {
				return name + ":" + p
			}
			return name + ":ok"
		},
		MCP:         map[string][]string{"gh": {"get_issue"}},
		MCPName:     func(s, t string) string { return "mcp__" + s + "__" + t },
		MCPDescribe: func(_ context.Context, s, t string) string { return "desc " + s + "." + t },
	}
}

func TestPrintAndReturn(t *testing.T) {
	res := Run(context.Background(), `print("a", 1); return {x: 2}`, testEnv(), Config{})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if !strings.HasPrefix(res.Output, "a 1\n") || !strings.Contains(res.Output, `"x": 2`) {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestToolsAndMCPBridge(t *testing.T) {
	res := Run(context.Background(), `
print(tools.Read({path: "a.go"}));
print(tools.read({path: "b.go"}));
print(mcp.gh.get_issue({path: "1"}));
print(mcp.describe("gh.get_issue"));
print(JSON.stringify(mcp.list()));`, testEnv(), Config{})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := "Read:a.go\nRead:b.go\nmcp__gh__get_issue:1\ndesc gh.get_issue\n"
	if !strings.HasPrefix(res.Output, want) || res.Calls != 3 {
		t.Fatalf("output = %q calls=%d", res.Output, res.Calls)
	}
}

func TestNoAmbientCapabilities(t *testing.T) {
	res := Run(context.Background(), `
return [typeof require, typeof process, typeof fetch, typeof setTimeout, typeof XMLHttpRequest, typeof os, typeof fs].join(",")`, testEnv(), Config{})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Output != strings.Repeat("undefined,", 6)+"undefined" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestBridgesAreFrozen(t *testing.T) {
	res := Run(context.Background(), `"use strict"; tools.Read = function(){ return "x" }`, testEnv(), Config{})
	if res.Err == nil {
		t.Fatal("assigning to tools must fail")
	}
}

func TestTimeoutInterruptsLoop(t *testing.T) {
	start := time.Now()
	res := Run(context.Background(), `while(true){}`, testEnv(), Config{Timeout: 300 * time.Millisecond})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("err = %v", res.Err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("loop was not interrupted promptly")
	}
}

func TestCancelInterrupts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := Run(ctx, `while(true){}`, testEnv(), Config{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "cancelled") {
		t.Fatalf("err = %v", res.Err)
	}
}

func TestBuildGuards(t *testing.T) {
	res := Run(context.Background(), `"x".repeat(1e9)`, testEnv(), Config{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "limit") {
		t.Fatalf("err = %v", res.Err)
	}
	res = Run(context.Background(), `"x".padEnd(1e9)`, testEnv(), Config{})
	if res.Err == nil {
		t.Fatal("padEnd must be guarded")
	}
}

func TestCallBudget(t *testing.T) {
	res := Run(context.Background(), `for (var i=0;i<10;i++) tools.Read({path:"a"})`, testEnv(), Config{MaxCalls: 3})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "budget") {
		t.Fatalf("err = %v", res.Err)
	}
	if res.Calls < 3 {
		t.Fatalf("calls = %d", res.Calls)
	}
}

func TestOutputCap(t *testing.T) {
	res := Run(context.Background(), `for (var i=0;i<1000;i++) print("line "+i)`, testEnv(), Config{MaxOutput: 100})
	if !res.Truncated || len(res.Output) > 100 || res.Err == nil {
		t.Fatalf("truncated=%v len=%d err=%v", res.Truncated, len(res.Output), res.Err)
	}
}

func TestScriptSizeAndEmpty(t *testing.T) {
	if res := Run(context.Background(), strings.Repeat("a", 100), testEnv(), Config{MaxScript: 10}); res.Err == nil {
		t.Fatal("oversized script must fail")
	}
	if res := Run(context.Background(), "  \n", testEnv(), Config{}); res.Err == nil {
		t.Fatal("empty script must fail")
	}
}

func TestSyntaxAndRuntimeErrorsCarryLines(t *testing.T) {
	res := Run(context.Background(), "var a = 1;\nvar b = ;", testEnv(), Config{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "script error") {
		t.Fatalf("err = %v", res.Err)
	}
	res = Run(context.Background(), "print('ok');\nundefinedFn()", testEnv(), Config{})
	if res.Err == nil || !strings.Contains(res.Err.Error(), ":2:") || res.Output != "ok" {
		t.Fatalf("err = %v out=%q", res.Err, res.Output)
	}
}

func TestParallelReadOnlyConcurrentMutatingSequential(t *testing.T) {
	var running, peak, editRunning atomic.Int32
	var overlap atomic.Bool
	env := testEnv()
	env.Call = func(_ context.Context, name string, _ map[string]any) string {
		if name == "Edit" {
			if running.Load() != 0 || editRunning.Add(1) != 1 {
				overlap.Store(true)
			}
			time.Sleep(10 * time.Millisecond)
			editRunning.Add(-1)
			return "edited"
		}
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return name
	}
	res := Run(context.Background(), `
var r = tools.parallel([["Read",{}],["Grep",{}],{tool:"Edit",args:{}},{tool:"Edit",args:{}},["Read",{}]]);
return r.join(",")`, env, Config{})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Output != "Read,Grep,edited,edited,Read" {
		t.Fatalf("order broken: %q", res.Output)
	}
	if peak.Load() < 2 {
		t.Fatal("read-only calls did not overlap")
	}
	if overlap.Load() {
		t.Fatal("a mutating call overlapped another call")
	}
}

func TestParallelRejectsUnknownTool(t *testing.T) {
	res := Run(context.Background(), `tools.parallel([["Nope",{}]])`, testEnv(), Config{})
	if res.Err == nil {
		t.Fatal("unknown tool must fail")
	}
}

func TestDescribeUnknownMCPTool(t *testing.T) {
	res := Run(context.Background(), `mcp.describe("gh.nope")`, testEnv(), Config{})
	if res.Err == nil {
		t.Fatal("unknown MCP tool must fail")
	}
}
