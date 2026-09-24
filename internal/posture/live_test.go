package posture

import (
	"sync"
	"testing"
)

func TestLiveNilReceiver(t *testing.T) {
	var l *Live
	if l.Policy().Level(ToolResultUnsafe) != Enforce {
		t.Fatal("nil receiver Policy should fall back to Defaults")
	}
	if l.Level(PermissionNoMatch) != Ignore {
		t.Fatal("nil receiver Level should fall back to DefaultLevel")
	}
	if l.AskDisabled() {
		t.Fatal("nil receiver AskDisabled should be false")
	}
}

func TestLiveSetCopiesPolicy(t *testing.T) {
	p := Policy{ToolResultUnsafe: Warn}
	l := NewLive(p, false)
	// Mutating the caller's own policy afterwards must not reach a reader.
	p[ToolResultUnsafe] = Ignore
	if got := l.Level(ToolResultUnsafe); got != Warn {
		t.Fatalf("caller mutation leaked into Live: got %s", got)
	}
}

func TestLivePolicyReturnsCopy(t *testing.T) {
	l := NewLive(Policy{ToolResultUnsafe: Warn}, false)
	got := l.Policy()
	got[ToolResultUnsafe] = Ignore
	if l.Level(ToolResultUnsafe) != Warn {
		t.Fatal("mutating the returned Policy mutated the Live")
	}
}

func TestLiveAskDisabled(t *testing.T) {
	l := NewLive(nil, false)
	if l.AskDisabled() {
		t.Fatal("expected ask enabled")
	}
	l.Set(nil, true)
	if !l.AskDisabled() {
		t.Fatal("expected ask disabled after Set")
	}
}

func TestLiveConcurrentSetLevel(t *testing.T) {
	// -race exercises the concurrent Set/Level path: Set is called on one
	// goroutine while many readers call Level. A data race here would mean the
	// map pointer hand-off is not actually atomic.
	l := NewLive(Defaults(), false)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = l.Level(ToolResultUnsafe)
				_ = l.Policy()
				_ = l.AskDisabled()
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			l.Set(AllIgnore(), true)
		} else {
			l.Set(Defaults(), false)
		}
	}
	close(stop)
	wg.Wait()
}

func TestLiveLevelFallsBackToDefault(t *testing.T) {
	l := NewLive(Policy{}, false)
	if l.Level(ToolResultUnsafe) != Enforce {
		t.Fatal("empty policy should fall back to default enforce")
	}
	if l.Level(PermissionNoMatch) != Ignore {
		t.Fatal("empty policy should fall back to default ignore")
	}
}

func TestLiveFixed(t *testing.T) {
	l := Fixed(Policy{ToolResultUnsafe: Warn})
	if l.Level(ToolResultUnsafe) != Warn {
		t.Fatalf("Fixed level = %q, want warn", l.Level(ToolResultUnsafe))
	}
	if l.AskDisabled() {
		t.Fatal("Fixed should have ask reporting on (AskDisabled false)")
	}
}
