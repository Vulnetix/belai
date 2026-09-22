package rolemanager

import (
	"testing"
	"time"
)

func TestRecordReachesObserver(t *testing.T) {
	got := make(chan Activity, 1)
	cancel := SetObserver(func(a Activity) {
		select {
		case got <- a:
		default:
		}
	})
	defer cancel()

	record(EventSecuritySentinel, string(SentinelSafe), "prompt", "", 0)

	select {
	case a := <-got:
		if a.Event != EventSecuritySentinel || a.Verdict != string(SentinelSafe) || a.Subject != "prompt" {
			t.Fatalf("activity = %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not receive the activity")
	}
}

func TestSetObserverNilDetaches(t *testing.T) {
	got := make(chan Activity, 1)
	cancel := SetObserver(func(a Activity) { got <- a })
	cancel()
	SetObserver(nil)

	record(EventBoundarySeal, "", "", "blocks=1", 0)
	select {
	case <-got:
		t.Fatal("a detached observer must not receive activity")
	default:
	}
}

func TestObserverCancelDetaches(t *testing.T) {
	got := make(chan Activity, 1)
	cancel := SetObserver(func(a Activity) { got <- a })
	cancel()

	record(EventSessionName, "valid", "", "", 0)
	select {
	case <-got:
		t.Fatal("a cancelled observer must not receive activity")
	default:
	}
}

func TestPanickingObserverDoesNotBreakRecord(t *testing.T) {
	SetObserver(func(Activity) { panic("observer boom") })
	defer SetObserver(nil)

	// record must recover the panic and still return.
	record(EventToolCallMismatch, string(PolicyAbort), "Bash", "abort", 0)
}

func TestSlowObserverDoesNotBreakRecord(t *testing.T) {
	done := make(chan struct{})
	SetObserver(func(Activity) {
		time.Sleep(10 * time.Millisecond)
		close(done)
	})
	defer SetObserver(nil)

	record(EventCompactionSummary, "valid", "", "", 0)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer was not invoked")
	}
}
