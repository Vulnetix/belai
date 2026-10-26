package vulnetixcli

import (
	"errors"
	"testing"

	"github.com/vulnetix/belai/internal/desktop"
)

// stubOpener replaces the process launcher and the environment for one test
// and returns the argv lists the opener was asked to start.
func stubOpener(t *testing.T, env desktop.Env) *[][]string {
	t.Helper()
	var started [][]string
	prevEnv, prevStart := openEnv, startOpener
	openEnv = func() desktop.Env { return env }
	startOpener = func(name string, args ...string) error {
		started = append(started, append([]string{name}, args...))
		return nil
	}
	t.Cleanup(func() { openEnv, startOpener = prevEnv, prevStart })
	return &started
}

func envOf(goos string, vars map[string]string) desktop.Env {
	return desktop.Env{
		Getenv:   func(k string) string { return vars[k] },
		LookPath: func(string) (string, error) { return "/usr/bin/xdg-open", nil },
		GOOS:     goos,
	}
}

func TestOpenBrowserStartsNothingWithoutADesktop(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"detached daemon": {"DISPLAY": ":0", desktop.EnvRCDetached: "1"},
		"forced off":      {"DISPLAY": ":0", desktop.EnvNoBrowser: "1"},
		"headless":        {},
	} {
		t.Run(name, func(t *testing.T) {
			started := stubOpener(t, envOf("linux", vars))
			err := OpenBrowser("https://www.vulnetix.com/resolve/belai-sessions")
			var nd *NoDesktopError
			if !errors.As(err, &nd) || nd.Reason == "" {
				t.Fatalf("err = %v, want a *NoDesktopError with a reason", err)
			}
			if len(*started) != 0 {
				t.Fatalf("started %v without a desktop", *started)
			}
		})
	}
}

func TestOpenBrowserStartsTheOpenerOnADesktop(t *testing.T) {
	started := stubOpener(t, envOf("linux", map[string]string{"DISPLAY": ":0"}))
	if err := OpenBrowser("https://www.vulnetix.com/x"); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 1 || (*started)[0][len((*started)[0])-1] != "https://www.vulnetix.com/x" {
		t.Fatalf("started = %v", *started)
	}
}

func TestOpenBrowserChecksTheURLBeforeTheEnvironment(t *testing.T) {
	started := stubOpener(t, envOf("linux", map[string]string{"DISPLAY": ":0"}))
	if err := OpenBrowser("http://insecure"); err == nil || errors.As(err, new(*NoDesktopError)) {
		t.Fatalf("err = %v, want the non-https refusal", err)
	}
	if len(*started) != 0 {
		t.Fatalf("started %v", *started)
	}
}
