package main

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/calltrace"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionctl"
)

// Without web controls: an unset ask leaves AskDisabled to the settings (nil),
// off resolves asks to allow, on keeps them from being allowed.
func TestRCAskDisabled(t *testing.T) {
	if rcAskDisabled(nil) != nil {
		t.Fatal("unset ask changed AskDisabled")
	}
	on, off := true, false
	if got := rcAskDisabled(&off); got == nil || !*got {
		t.Errorf("ask off: AskDisabled = %v, want true", got)
	}
	if got := rcAskDisabled(&on); got == nil || *got {
		t.Errorf("ask on: AskDisabled = %v, want false", got)
	}
}

// With web controls the request's ask is the session's starting ask control,
// over every settings layer; unset keeps the resolved ask_permission.
func TestRCInitialAskOverridesSettings(t *testing.T) {
	on, off := true, false
	no := false
	for _, c := range []struct {
		name     string
		settings config.Settings
		ask      *bool
		want     bool
	}{
		{"default, unset", config.Settings{}, nil, true},
		{"settings off, unset", config.Settings{AskPermission: &no}, nil, false},
		{"default, off", config.Settings{}, &off, false},
		{"settings off, on", config.Settings{AskPermission: &no}, &on, true},
	} {
		st := sessionctl.FromSettings(c.settings, "agent", "p", "m", "")
		if got := rcInitialAsk(st.Ask, c.ask); got != c.want {
			t.Errorf("%s: ask = %v, want %v", c.name, got, c.want)
		}
	}
}

// -ask takes on or off only, and a pix-assistant session takes none.
func TestRCSessionAskFlag(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	base := []string{"-dispatch", "d1", "-session-id", "s1"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-ask", "maybe"}, "-ask must be on or off"},
		{[]string{"-mode", "pix-assistant", "-profile", "patcher", "-ask", "off"}, "takes no -ask"},
	} {
		var stderr bytes.Buffer
		code := runRCSessionCLI(context.Background(), append(slices.Clone(base), c.args...), strings.NewReader("hi"), &stderr)
		if code != 2 || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", c.args, code, stderr.String(), c.want)
		}
	}
}

// The cleaned trigger becomes the process's BELAI_SESSION_TRIGGER and reaches
// tool and hook environments through calltrace; "" clears both.
func TestApplyTriggerEnv(t *testing.T) {
	t.Setenv(calltrace.EnvTrigger, "stale")
	t.Cleanup(func() { calltrace.SetTrigger("") })
	const v = `{"source":"console"}`
	applyTriggerEnv(v)
	if got, _ := os.LookupEnv(calltrace.EnvTrigger); got != v {
		t.Errorf("process env = %q", got)
	}
	if !slices.Contains(calltrace.Env(context.Background()), calltrace.EnvTrigger+"="+v) {
		t.Errorf("tool env lacks the trigger: %v", calltrace.Env(context.Background()))
	}
	applyTriggerEnv("")
	if _, ok := os.LookupEnv(calltrace.EnvTrigger); ok {
		t.Error("process env kept the trigger")
	}
	for _, e := range calltrace.Env(context.Background()) {
		if strings.HasPrefix(e, calltrace.EnvTrigger+"=") {
			t.Errorf("tool env kept the trigger: %s", e)
		}
	}
}
