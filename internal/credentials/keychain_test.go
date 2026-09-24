package credentials

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestKeyringBackendNameAndReason covers the zero-value surface of the real
// keyring backend: its name, service binding, and the unavailable-reason text
// that Backends() surfaces to the TUI.
func TestKeyringBackendNameAndReason(t *testing.T) {
	k := NewKeyringBackend("svc").(*keyringBackend)
	if k.Name() != "keychain" {
		t.Fatalf("Name() = %q, want keychain", k.Name())
	}
	if k.service != "svc" {
		t.Fatalf("service = %q, want svc", k.service)
	}
	if got := k.reasonText(); got != "no Secret Service on this session bus" {
		t.Fatalf("reasonText(default) = %q", got)
	}
	k.reason = "dbus exploded"
	if got := k.reasonText(); got != "dbus exploded" {
		t.Fatalf("reasonText(set) = %q", got)
	}
	if newKeyringBackend().Name() != "keychain" {
		t.Fatal("default backend must also report name keychain")
	}
}

// TestKeyringBackendAvailableAgainstMock pins that a keyring answering
// "not found" still counts as available (the probe account never exists).
func TestKeyringBackendAvailableAgainstMock(t *testing.T) {
	keyring.MockInit()
	k := NewKeyringBackend("svc").(*keyringBackend)
	if !k.Available() {
		t.Fatalf("Available() = false with mock keyring (reason=%q)", k.reason)
	}
}

// TestKeyringBackendUnavailableReason pins that a hard keyring error marks the
// backend unavailable and records the reason verbatim.
func TestKeyringBackendUnavailableReason(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus exploded"))
	k := NewKeyringBackend("svc").(*keyringBackend)
	if k.Available() {
		t.Fatal("Available() = true with a failing keyring")
	}
	if k.reason != "dbus exploded" {
		t.Fatalf("reason = %q, want dbus exploded", k.reason)
	}
	if k.reasonText() != "dbus exploded" {
		t.Fatalf("reasonText() = %q", k.reasonText())
	}
}

// TestKeyringBackendSetGetDelete exercises the timeout-wrapped keyring
// operations end to end against the in-memory mock, including the mapping of
// keyring.ErrNotFound to the package's ErrNotFound.
func TestKeyringBackendSetGetDelete(t *testing.T) {
	keyring.MockInit()
	k := NewKeyringBackend("svc")

	if err := k.Set("acct", "secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, err := k.Get("acct"); err != nil || v != "secret" {
		t.Fatalf("Get = (%q, %v), want secret", v, err)
	}
	if _, err := k.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}
	if err := k.Delete("acct"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get("acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}
