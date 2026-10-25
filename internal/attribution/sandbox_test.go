package attribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAttr(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "attribution")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

func TestLoadReadsThePersonAndTheSignOff(t *testing.T) {
	p := writeAttr(t, "Co-authored-by: Ada Lovelace <ada@example.com>\nSigned-off-by: Pix [bot] <security@vulnetix.com>\n")
	c, ok := Load(p)
	if !ok || c.Bot != Pix || c.Human == nil || c.Human.Name != "Ada Lovelace" || c.Human.Email != "ada@example.com" {
		t.Fatalf("got %+v ok=%v", c, ok)
	}
}

func TestLoadTreatsABotCoAuthorAsNoPerson(t *testing.T) {
	p := writeAttr(t, "Co-authored-by: vulnetix[bot] <176591487+vulnetix[bot]@users.noreply.github.com>\nSigned-off-by: Pix [bot] <security@vulnetix.com>\n")
	c, ok := Load(p)
	if !ok || c.Human != nil {
		t.Fatalf("got %+v ok=%v", c, ok)
	}
}

func TestOutsideASandboxNothingIsAdded(t *testing.T) {
	t.Setenv(FileEnv, filepath.Join(t.TempDir(), "absent"))
	if got := Message("feat: x", "ses_1"); got != "feat: x" {
		t.Fatalf("got %q", got)
	}
	if got := Markdown("body", "ses_1"); got != "body" {
		t.Fatalf("got %q", got)
	}
}

func TestMessageInsideASandbox(t *testing.T) {
	t.Setenv(FileEnv, writeAttr(t, "Co-authored-by: Ada Lovelace <ada@example.com>\nSigned-off-by: Pix [bot] <security@vulnetix.com>\n"))
	got := Message("fix: x\n\nbody", "ses_1", "CVE-2021-44228")
	for _, want := range []string{
		"Co-authored-by: Ada Lovelace <ada@example.com>",
		"Signed-off-by: Pix [bot] <security@vulnetix.com>",
		"Session: https://vulnetix.com/resolve/factory-session?id=ses_1",
		"Vulnerability: https://vulnetix.com/vdb/CVE-2021-44228",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if Message(got, "ses_1", "CVE-2021-44228") != got {
		t.Fatal("a second pass must add nothing")
	}
}

func TestMissingAndShellArgs(t *testing.T) {
	c := Context{Bot: Pix}
	if !Missing("feat: x", c) || Missing("feat: x\n\nSigned-off-by: Pix [bot] <security@vulnetix.com>", c) {
		t.Fatal("Missing")
	}
	args := ShellTrailerArgs(Context{Bot: Pix, Human: &Human{Name: "Ada O'Neil", Email: "ada@example.com"}})
	if !strings.Contains(args, `--trailer 'Co-authored-by: Ada O'\''Neil <ada@example.com>'`) {
		t.Fatalf("quoting: %s", args)
	}
}
