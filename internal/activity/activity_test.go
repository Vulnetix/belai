package activity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	cancelled := false
	h := r.Add(Activity{Kind: KindVulnetix, Label: "vulnetix scan", Argv: []string{"vulnetix", "scan"}}, func() { cancelled = true })
	if h.Activity.State != StateQueued {
		t.Fatalf("state = %q, want queued", h.Activity.State)
	}
	if h.Activity.ID == "" {
		t.Fatal("ID must be assigned")
	}

	h.Activity.State = StateRunning
	h.Append("line one")
	h.Append("line two")
	h.Finish(0, false, nil)

	list := r.List()
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].State != StateDone || list[0].ExitCode != 0 {
		t.Fatalf("list[0] = %+v, want done", list[0])
	}
	if out := h.Output(); !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Fatalf("output = %q", out)
	}
	if ring := h.Ring(); len(ring) != 2 {
		t.Fatalf("ring = %v, want 2 lines", ring)
	}
	if cancelled {
		t.Fatal("cancel must not be called on normal finish")
	}
}

func TestRegistryKill(t *testing.T) {
	r := NewRegistry()
	cancelled := false
	h := r.Add(Activity{Kind: KindShell, Label: "!git status"}, func() { cancelled = true })
	h.Activity.State = StateRunning

	if err := r.Kill(h.Activity.ID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !cancelled {
		t.Fatal("Kill must call the cancel func")
	}
	if h.Activity.State != StateKilled {
		t.Fatalf("state = %q, want killed", h.Activity.State)
	}
	if err := r.Kill("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Kill unknown = %v, want ErrNotFound", err)
	}
}

func TestRegistryOutputCap(t *testing.T) {
	h := &Handle{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			h.Append(strings.Repeat("x", 4096))
		}
	}()
	wg.Wait()
	if len(h.Output()) > MaxOutputBytes+128 {
		t.Fatalf("output too large: %d", len(h.Output()))
	}
	if got := len(h.Ring()); got > ringLines {
		t.Fatalf("ring len = %d, want <= %d", got, ringLines)
	}
}

func TestRegistryConcurrentAppend(t *testing.T) {
	h := &Handle{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Append("line")
			}
		}(i)
	}
	wg.Wait()
	if h.Ring() == nil {
		t.Fatal("ring must not be nil after concurrent appends")
	}
}

func TestRegistryEvents(t *testing.T) {
	r := NewRegistry()
	h := r.Add(Activity{Kind: KindAgent, Label: "belai:triage"}, nil)
	select {
	case e := <-r.Events():
		if e.ID != h.Activity.ID {
			t.Fatalf("event id = %q, want %q", e.ID, h.Activity.ID)
		}
	default:
		t.Fatal("Add must emit an event")
	}
}

func TestFinishDoesNotOverwriteKilled(t *testing.T) {
	r := NewRegistry()
	h := r.Add(Activity{Kind: KindShell, Label: "x"}, context.CancelFunc(func() {}))
	h.Activity.State = StateRunning
	_ = r.Kill(h.Activity.ID)
	h.Finish(0, false, nil)
	if h.Activity.State != StateKilled {
		t.Fatalf("state = %q, want killed (kill wins over finish)", h.Activity.State)
	}
}

// TestRegistryAppendByID verifies that callers holding only the activity id
// can append output without keeping the Add handle.
func TestRegistryAppendByID(t *testing.T) {
	r := NewRegistry()
	h := r.Add(Activity{Kind: KindProcess, Label: "!!sleep 30"}, nil)
	h.Activity.State = StateRunning
	r.Append(h.Activity.ID, "first")
	r.Append(h.Activity.ID, "second")
	if out := r.Output(h.Activity.ID); !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("output = %q, want both lines", out)
	}
	r.Append("unknown-id", "ignored")
	if out := r.Output(h.Activity.ID); strings.Contains(out, "ignored") {
		t.Fatalf("unknown id must not append: %q", out)
	}
}

// TestRegistryFinishByID covers the id-only Finish path (the direct-handle
// path is exercised everywhere else). Unknown ids must be ignored.
func TestRegistryFinishByID(t *testing.T) {
	r := NewRegistry()
	h := r.Add(Activity{Kind: KindVulnetix, Label: "scan"}, nil)
	h.Activity.State = StateRunning
	r.Finish(h.Activity.ID, 1, true, errors.New("boom"))
	if h.Activity.State != StateFailed || h.Activity.ExitCode != 1 || !h.Activity.TimedOut {
		t.Fatalf("state = %q exit=%d timedOut=%v, want failed/1/true", h.Activity.State, h.Activity.ExitCode, h.Activity.TimedOut)
	}
	// A clean id-only finish maps to done.
	h2 := r.Add(Activity{Kind: KindVulnetix, Label: "scan2"}, nil)
	h2.Activity.State = StateRunning
	r.Finish(h2.Activity.ID, 0, false, nil)
	if h2.Activity.State != StateDone || h2.Activity.TimedOut {
		t.Fatalf("state = %q timedOut=%v, want done/false", h2.Activity.State, h2.Activity.TimedOut)
	}
	// Unknown id is a no-op, not a panic.
	r.Finish("nope", 0, false, nil)
}

// TestSetTargetsEmitsEvent covers SetTargets and its event emission, plus the
// defensive copy it makes of the caller's slice.
func TestSetTargetsEmitsEvent(t *testing.T) {
	r := NewRegistry()
	h := r.Add(Activity{Kind: KindVulnetix, Label: "scan"}, nil)
	<-r.Events() // drain the Add event

	src := []string{"out.json"}
	h.SetTargets(src)
	src[0] = "mutated.json" // must not leak into the stored copy

	if len(h.Activity.Targets) != 1 || h.Activity.Targets[0] != "out.json" {
		t.Fatalf("targets = %v, want a defensive copy of [out.json]", h.Activity.Targets)
	}
	select {
	case e := <-r.Events():
		if len(e.Targets) != 1 || e.Targets[0] != "out.json" {
			t.Fatalf("event targets = %v", e.Targets)
		}
	default:
		t.Fatal("SetTargets must emit an event")
	}
}
