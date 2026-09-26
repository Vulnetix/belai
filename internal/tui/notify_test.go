package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/notify"
)

// events left out means the default set; an explicit empty list means none;
// an unknown name is ignored.
func TestNotifyWanted(t *testing.T) {
	if !notifyWanted(nil, notify.EventPermission) || notifyWanted(nil, notify.EventTurnDone) {
		t.Fatal("nil events must mean the default set (permission yes, turn_done no)")
	}
	if notifyWanted([]string{}, notify.EventPermission) {
		t.Fatal("empty events must mean none")
	}
	if !notifyWanted([]string{"bogus", notify.EventTurnDone}, notify.EventTurnDone) || notifyWanted([]string{"bogus"}, notify.EventPermission) {
		t.Fatal("unknown names must be ignored")
	}
}
