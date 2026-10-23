package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/tools"
)

// A pix-assistant session is refused before it reads a prompt unless it names
// one of the three personas, and it never takes the host's web opt-ins or git
// sync; outside that mode a persona is held to the ordinary -profile rule.
func TestRCSessionPixAssistantFlags(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	base := []string{"-dispatch", "d1", "-session-id", "s1"}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-mode", "pix-assistant"}, "needs -profile vuln-scout, patcher or verifier"},
		{[]string{"-mode", "pix-assistant", "-profile", "reviewer"}, "needs -profile vuln-scout, patcher or verifier"},
		{[]string{"-mode", "pix-assistant", "-profile", "belai:builder"}, "needs -profile vuln-scout, patcher or verifier"},
		{[]string{"-mode", "pix-assistant", "-profile", "patcher", "-controls"}, "takes no -controls or -shell"},
		{[]string{"-mode", "pix-assistant", "-profile", "patcher", "-shell"}, "takes no -controls or -shell"},
		{[]string{"-mode", "pix-assistant", "-profile", "patcher", "-git-sync", "on"}, "runs with -git-sync off"},
		// Outside pix mode a persona is just a profile: it needs agent mode.
		{[]string{"-mode", "plan", "-profile", "belai:vuln-scout"}, "-profile needs -mode agent"},
	} {
		var stderr bytes.Buffer
		code := runRCSessionCLI(context.Background(), append(slices.Clone(base), c.args...), strings.NewReader("hi"), &stderr)
		if code != 2 || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", c.args, code, stderr.String(), c.want)
		}
	}
}

func TestRCPixStartLoadsTheReadOnlyPersona(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	for _, name := range []string{"vuln-scout", "belai:patcher", "verifier"} {
		var stderr bytes.Buffer
		ps, code := rcPixStart(name, "off", false, false, &stderr)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, stderr.String())
		}
		if !strings.HasPrefix(ps.Name, "belai:") || ps.Persona == "" {
			t.Fatalf("%s: %+v", name, ps)
		}
	}
}

// The persona's tools are narrowed to the read-only ceiling through the same
// narrowing an engaged profile uses: Kremvax keeps no Edit, Write or Bash.
func TestRCPixNarrowKeepsOnlyReadOnlyTools(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	ps, err := rc.LoadPixPersona("patcher")
	if err != nil {
		t.Fatal(err)
	}
	full := tools.Default(t.TempDir(), false)
	narrow := rcNarrow(ps.Tools, ps.Skills)
	if narrow == nil {
		t.Fatal("a pix persona must always narrow")
	}
	got := narrow(full).Names()
	for _, n := range got {
		// The repo memory tools are core: no narrowing removes them.
		if !slices.Contains(rc.PixTools, n) && !slices.Contains(tools.CoreToolNames, n) {
			t.Errorf("%s survived the narrowing: %v", n, got)
		}
	}
	for _, want := range []string{"Read", "Grep", "Glob"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s was dropped: %v", want, got)
		}
	}
}
