package desktop

import (
	"errors"
	"strings"
	"testing"
)

func testEnv(goos string, vars map[string]string, haveOpener bool) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		LookPath: func(name string) (string, error) {
			if haveOpener && name == "xdg-open" {
				return "/usr/bin/xdg-open", nil
			}
			return "", errors.New("not found")
		},
		GOOS: goos,
	}
}

func TestCanOpenBrowser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		goos   string
		vars   map[string]string
		opener bool
		want   bool
		reason string
	}{
		{"x11 desktop", "linux", map[string]string{"DISPLAY": ":0"}, true, true, ""},
		{"wayland only", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true, true, ""},
		{"headless linux", "linux", map[string]string{}, true, false, "no graphical session"},
		{"desktop without xdg-open", "linux", map[string]string{"DISPLAY": ":0"}, false, false, "xdg-open"},
		{"ssh session", "linux", map[string]string{"DISPLAY": ":0", "SSH_CONNECTION": "1.2.3.4 1 5.6.7.8 22"}, true, false, "SSH"},
		{"ssh tty on mac", "darwin", map[string]string{"SSH_TTY": "/dev/pts/1"}, false, false, "SSH"},
		{"detached rc daemon", "linux", map[string]string{"DISPLAY": ":0", EnvRCDetached: "1"}, true, false, "detached"},
		{"forced off", "darwin", map[string]string{EnvNoBrowser: "1"}, false, false, EnvNoBrowser},
		{"forced off with 0 is on", "darwin", map[string]string{EnvNoBrowser: "0"}, false, true, ""},
		{"non-interactive", "linux", map[string]string{"DISPLAY": ":0", EnvNoTUI: "1"}, true, false, "non-interactive"},
		{"ci", "linux", map[string]string{"DISPLAY": ":0", "CI": "true"}, true, false, "CI"},
		{"mac needs no display", "darwin", map[string]string{}, false, true, ""},
		{"windows needs no display", "windows", map[string]string{}, false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := testEnv(tc.goos, tc.vars, tc.opener).CanOpenBrowser()
			if ok != tc.want {
				t.Fatalf("CanOpenBrowser = %v (%q), want %v", ok, reason, tc.want)
			}
			if !strings.Contains(reason, tc.reason) {
				t.Fatalf("reason %q does not mention %q", reason, tc.reason)
			}
			if ok && reason != "" {
				t.Fatalf("an allowed run has a reason: %q", reason)
			}
		})
	}
}

func TestSystemEnvIsWired(t *testing.T) {
	e := SystemEnv()
	if e.Getenv == nil || e.LookPath == nil || e.GOOS == "" {
		t.Fatalf("SystemEnv left a field unset: %+v", e)
	}
}
