package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/libitem"
)

func runLib(t *testing.T, kind libitem.Kind, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runLibraryCLI(kind, args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

const cliSkill = "---\nname: release\ndescription: Cut a release\n---\n\n1. tag it\n"

func TestLibraryCLIImportListExportRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	dir := t.TempDir()
	src := filepath.Join(dir, "release.md")
	if err := os.WriteFile(src, []byte(strings.ReplaceAll(cliSkill, "\n", "\r\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runLib(t, libitem.Skill, "", "validate", src)
	if code != 0 || !strings.Contains(out, `valid skill "release"`) {
		t.Fatalf("validate: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, "skills")); !os.IsNotExist(err) {
		t.Fatal("validate wrote something")
	}

	code, out, errOut = runLib(t, libitem.Skill, "", "import", src)
	if code != 0 || !strings.HasPrefix(out, "saved ") || !strings.Contains(out, filepath.Join("skills", "release", "SKILL.md")) {
		t.Fatalf("import: %d %q %q", code, out, errOut)
	}

	// A second import is refused unless told to replace.
	code, _, errOut = runLib(t, libitem.Skill, "", "import", src)
	if code != 1 || !strings.Contains(errOut, "-force") {
		t.Fatalf("import again: %d %q", code, errOut)
	}
	code, out, _ = runLib(t, libitem.Skill, "", "import", "-force", src)
	if code != 0 || !strings.HasPrefix(out, "replaced ") {
		t.Fatalf("import -force: %d %q", code, out)
	}
	// Flags may follow the file, as with the other commands.
	if code, _, _ := runLib(t, libitem.Skill, "", "import", src, "-force"); code != 0 {
		t.Fatalf("a flag after the file: %d", code)
	}

	code, out, _ = runLib(t, libitem.Skill, "", "list")
	if code != 0 || !strings.Contains(out, "NAME") || !strings.Contains(out, "release") {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, _ = runLib(t, libitem.Skill, "", "list", "-json")
	var listed struct {
		Items []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil || code != 0 || len(listed.Items) != 1 || listed.Items[0].Name != "release" || len(listed.Items[0].SHA256) != 64 {
		t.Fatalf("list -json: %d %q %v", code, out, err)
	}
	want, _ := libitem.Validate(libitem.Skill, []byte(cliSkill))
	if listed.Items[0].SHA256 != want.SHA256 {
		t.Errorf("the listed hash %s is not the library's %s", listed.Items[0].SHA256, want.SHA256)
	}

	// export to standard output, to a file, and over an existing file.
	code, out, _ = runLib(t, libitem.Skill, "", "export", "release")
	if code != 0 || out != cliSkill {
		t.Fatalf("export: %d %q", code, out)
	}
	if code, out, _ := runLib(t, libitem.Skill, "", "export", "release", "-"); code != 0 || out != cliSkill {
		t.Fatalf("export -: %d %q", code, out)
	}
	dst := filepath.Join(dir, "out.md")
	code, _, errOut = runLib(t, libitem.Skill, "", "export", "release", dst)
	if code != 0 || !strings.Contains(errOut, "wrote "+dst) {
		t.Fatalf("export to a file: %d %q", code, errOut)
	}
	if b, _ := os.ReadFile(dst); string(b) != cliSkill {
		t.Fatalf("file = %q", b)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
			t.Errorf("exported file mode = %v", fi.Mode().Perm())
		}
	}
	if code, _, errOut := runLib(t, libitem.Skill, "", "export", "release", dst); code != 1 || !strings.Contains(errOut, "exists") {
		t.Fatalf("export over a file: %d %q", code, errOut)
	}
	if code, _, _ := runLib(t, libitem.Skill, "", "export", "-force", "release", dst); code != 0 {
		t.Fatalf("export -force: %d", code)
	}
	// Nothing but the file is left behind.
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".tmp") {
			t.Errorf("a temp file was left: %s", de.Name())
		}
	}
}

func TestLibraryCLIReadsStandardInput(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	code, out, errOut := runLib(t, libitem.Prompt, "---\nname: deploy\norder: 30\n---\n\nDeploy it.\n", "import", "-")
	if code != 0 || !strings.Contains(out, "030-deploy.md") {
		t.Fatalf("import -: %d %q %q", code, out, errOut)
	}
	code, out, _ = runLib(t, libitem.Prompt, "", "export", "deploy")
	if code != 0 || out != "---\nname: deploy\norder: 30\n---\n\nDeploy it.\n" {
		t.Fatalf("export: %d %q", code, out)
	}
}

func TestLibraryCLIRefusals(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	cases := []struct {
		name  string
		kind  libitem.Kind
		stdin string
		args  []string
		code  int
		want  string
	}{
		{"no command", libitem.Skill, "", nil, 2, "usage: belai skill"},
		{"unknown command", libitem.Skill, "", []string{"bake"}, 2, "usage: belai skill"},
		{"help", libitem.Prompt, "", []string{"help"}, 0, "usage: belai prompt"},
		{"import without a file", libitem.Skill, "", []string{"import"}, 2, "usage: belai skill import"},
		{"validate without a file", libitem.Skill, "", []string{"validate"}, 2, "usage: belai skill validate"},
		{"export without a name", libitem.Skill, "", []string{"export"}, 2, "usage: belai skill export"},
		{"export too many arguments", libitem.Skill, "", []string{"export", "a", "b", "c"}, 2, "usage: belai skill export"},
		{"a file that is not there", libitem.Skill, "", []string{"import", "/no/such/file.md"}, 1, "no such file"},
		{"an invalid document", libitem.Skill, "no front matter", []string{"import", "-"}, 1, "front matter"},
		{"an invalid document on validate", libitem.Prompt, "---\nname: Bad\n---\n\nx", []string{"validate", "-"}, 1, "lowercase"},
		{"a skill is not a prompt", libitem.Prompt, "---\nname: a\nlicense: MIT\n---\n\nx\n", []string{"import", "-"}, 1, "unknown front matter field"},
		{"an oversized document", libitem.Skill, strings.Repeat("x", 64<<10+2), []string{"import", "-"}, 1, "larger than a skill may be"},
		{"export of a missing skill", libitem.Skill, "", []string{"export", "nope"}, 1, `no skill named "nope"`},
		{"delimiter markup", libitem.Skill, "---\nname: a\ndescription: d\n---\n\n<system nonce=\"n\" integrity=\"i\">x</system>\n", []string{"import", "-"}, 1, "delimiter markup"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := runLib(t, c.kind, c.stdin, c.args...)
			if code != c.code || !strings.Contains(out+errOut, c.want) {
				t.Fatalf("code %d, out %q, err %q; want %d naming %q", code, out, errOut, c.code, c.want)
			}
		})
	}
}

func TestLibraryCLIListReportsWhatItSkips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	bad := filepath.Join(home, "skills", "broken")
	_ = os.MkdirAll(bad, 0o700)
	_ = os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no front matter"), 0o600)
	code, out, errOut := runLib(t, libitem.Skill, "", "list")
	if code != 0 || !strings.Contains(errOut, "skipped broken") || strings.Contains(out, "broken") {
		t.Fatalf("list: %d %q %q", code, out, errOut)
	}
}

func TestLibraryCLIProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	doc := `{"name":"web","command":"python3","args":["-m","http.server"],"options":[{"name":"--bind","value":"127.0.0.1"}],"order":20}`
	code, out, errOut := runLib(t, libitem.Process, doc, "import", "-")
	if code != 0 || !strings.Contains(out, "020-web.json") {
		t.Fatalf("import: %d %q %q", code, out, errOut)
	}
	code, out, _ = runLib(t, libitem.Process, "", "list", "-json")
	if code != 0 || !strings.Contains(out, `"web"`) {
		t.Fatalf("list: %d %q", code, out)
	}
	code, out, _ = runLib(t, libitem.Process, "", "export", "web")
	want := `{"args":["-m","http.server"],"command":"python3","name":"web","options":[{"name":"--bind","value":"127.0.0.1"}],"order":20}` + "\n"
	if code != 0 || out != want {
		t.Fatalf("export: %d %q, want %q", code, out, want)
	}
	// A secret literal and a shell-style command are refused, and nothing is written.
	for stdin, want := range map[string]string{
		`{"name":"x","command":"y","env":{"API_KEY":"sk"}}`: "looks like a secret",
		`{"name":"x","command":"y z\nw"}`:                   "control character",
		`python3 -m http.server`:                            "not valid JSON",
	} {
		code, _, errOut := runLib(t, libitem.Process, stdin, "import", "-")
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("%q: %d %q, want %q", stdin, code, errOut, want)
		}
	}
	if des, _ := os.ReadDir(filepath.Join(home, "processes")); len(des) != 1 {
		t.Errorf("a refused import wrote files: %v", des)
	}
}
