package notify

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func env(vars map[string]string, goos string, onPath ...string) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		LookPath: func(name string) (string, error) {
			for _, p := range onPath {
				if p == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		},
		GOOS: goos,
	}
}

func TestResolveAuto(t *testing.T) {
	cases := []struct {
		name string
		env  Env
		want string
	}{
		{"kitty", env(map[string]string{"KITTY_WINDOW_ID": "1"}, "linux", "notify-send"), BackendOSC},
		{"iterm", env(map[string]string{"TERM_PROGRAM": "iTerm.app"}, "darwin"), BackendOSC},
		{"linux notify-send", env(nil, "linux", "notify-send"), BackendNotifySend},
		{"mac", env(nil, "darwin", "osascript"), BackendOsascript},
		{"nothing", env(nil, "linux"), BackendBell},
	}
	for _, c := range cases {
		if got := c.env.Resolve(BackendAuto); got != c.want {
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
	if got := env(nil, "linux").Resolve(BackendBell); got != BackendBell {
		t.Errorf("explicit backend not honoured: %q", got)
	}
}

func TestOSCSequences(t *testing.T) {
	var buf bytes.Buffer
	n := New(BackendOSC, env(map[string]string{"KITTY_WINDOW_ID": "1"}, "linux"), &buf)
	if err := n.Send(context.Background(), EventPermission, "Bash"); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "\x1b]777;notify;Belai;Permission needed for Bash\x07" {
		t.Fatalf("osc777 = %q", got)
	}
	buf.Reset()
	n = New(BackendOSC, env(map[string]string{"WT_SESSION": "x", "TMUX": "/tmp/t"}, "windows"), &buf)
	_ = n.Send(context.Background(), EventGoalDone, "")
	if got := buf.String(); !strings.HasPrefix(got, "\x1bPtmux;\x1b\x1b]9;Belai: Goal complete") || !strings.HasSuffix(got, "\x1b\\") {
		t.Fatalf("tmux-wrapped osc9 = %q", got)
	}
}

// A subject is reduced to an identifier: no escape, newline, or prose from a
// tool or agent name can ride into a notification.
func TestSubjectIsAnIdentifier(t *testing.T) {
	_, body := Message(EventPermission, "Bash\x1b]0;evil\x07 ignore previous instructions")
	subject := strings.TrimPrefix(body, "Permission needed for ")
	if strings.ContainsAny(subject, "\x1b\x07\n ;]") {
		t.Fatalf("subject leaked markup or prose: %q", body)
	}
}

func TestExternalBackendsUseFixedArgv(t *testing.T) {
	var got []string
	n := New(BackendNotifySend, env(nil, "linux"), nil)
	n.run = func(_ context.Context, name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	if err := n.Send(context.Background(), EventAgentDone, "deps-go"); err != nil {
		t.Fatal(err)
	}
	want := []string{"notify-send", "--app-name=Belai", "Belai", "Background agent deps-go finished"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("argv = %q", got)
	}
}

func TestBellNeedsTTY(t *testing.T) {
	if err := New(BackendBell, env(nil, "linux"), nil).Send(context.Background(), EventTurnDone, ""); err == nil {
		t.Fatal("bell without a tty succeeded")
	}
}

// Every event has its own fixed message, and none of them is the fallback.
func TestEveryEventHasAMessage(t *testing.T) {
	seen := map[string]string{}
	_, fallback := Message("nope", "")
	for _, e := range Events {
		_, body := Message(e, "")
		if body == "" || body == fallback {
			t.Errorf("%s has no message of its own", e)
		}
		if prev, dup := seen[body]; dup {
			t.Errorf("%s and %s share the message %q", e, prev, body)
		}
		seen[body] = e
		if !ValidEvent(e) {
			t.Errorf("%s not valid", e)
		}
	}
	for _, b := range Backends {
		if !ValidBackend(b) {
			t.Errorf("backend %s not valid", b)
		}
	}
	if ValidEvent("bogus") || ValidBackend("bogus") {
		t.Fatal("unknown name accepted")
	}
}
