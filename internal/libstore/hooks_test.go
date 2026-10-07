package libstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/posture"
)

func hookJSON(name, command string) string {
	b, _ := json.Marshal(map[string]any{
		"name": name, "description": "Guards " + name,
		"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": command}}}}},
	})
	return string(b)
}

func bundle(files map[string]string) []BundleFile {
	var out []BundleFile
	for p, c := range files {
		out = append(out, BundleFile{Path: p, Data: []byte(c)})
	}
	return out
}

func TestHookBundleInstallsAndHashesWithItsFiles(t *testing.T) {
	h := home(t)
	files := map[string]string{"guard.sh": "#!/bin/sh\necho '{}'\n", "rules/deny.txt": "rm -rf\n"}
	res, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", "guard.sh rules/deny.txt")), bundle(files), InstallOptions{Name: "guard"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	dir := filepath.Join(h, "hooks", "guard")
	if res.Where != dir {
		t.Errorf("Where = %q", res.Where)
	}
	def, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	want, _ := libitem.Validate(libitem.Hook, []byte(hookJSON("guard", "guard.sh rules/deny.txt")))
	if err != nil || string(def) != string(want.Doc) {
		t.Fatalf("definition = %q %v", def, err)
	}
	if runtime.GOOS != "windows" {
		for p, mode := range map[string]os.FileMode{
			filepath.Join(dir, "guard.sh"): 0o700, filepath.Join(dir, "rules", "deny.txt"): 0o600, filepath.Join(dir, "hooks.json"): 0o600, dir: 0o700,
		} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
				t.Errorf("%s mode = %v %v, want %v", p, fi.Mode().Perm(), err, mode)
			}
		}
	}
	items, skipped, err := List(libitem.Hook)
	if err != nil || len(items) != 1 || len(skipped) != 0 {
		t.Fatalf("list: %+v %+v %v", items, skipped, err)
	}
	it := items[0]
	if it.Name != "guard" || len(it.Files) != 2 || it.Files[0].Path != "guard.sh" || it.Files[1].Path != "rules/deny.txt" {
		t.Fatalf("item = %+v", it)
	}
	bf := []libitem.BundleFile{{Path: "guard.sh", SHA256: fileSum([]byte(files["guard.sh"]))}, {Path: "rules/deny.txt", SHA256: fileSum([]byte(files["rules/deny.txt"]))}}
	if it.SHA256 != libitem.HashBundle(want.Doc, bf) {
		t.Errorf("hash = %s, want the bundle hash", it.SHA256)
	}
	// The bundle runs: the loader reads what the library installed.
	hs, err := hooks.LoadBundles(filepath.Join(h, "hooks"), nilPolicy(), nil)
	if err != nil || len(hs) != 1 || hs[0].Name != "bundle:guard:PreToolUse:01-01" {
		t.Fatalf("loader: %+v %v", hs, err)
	}
	// Editing a script changes the hash: that is what drift sees.
	if err := os.WriteFile(filepath.Join(dir, "guard.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := List(libitem.Hook); again[0].SHA256 == it.SHA256 {
		t.Error("an edited script kept the hash")
	}
}

func TestHookBundleNeedsOverwriteAndReplacesWhole(t *testing.T) {
	h := home(t)
	if _, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", "a.sh")), bundle(map[string]string{"a.sh": "one\n", "old.txt": "x\n"}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", "b.sh")), bundle(map[string]string{"b.sh": "two\n"}), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("a second install: %v", err)
	}
	res, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", "b.sh")), bundle(map[string]string{"b.sh": "two\n"}), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	dir := filepath.Join(h, "hooks", "guard")
	for _, gone := range []string{"a.sh", "old.txt"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s survived the replacement", gone)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(h, "hooks"))
	if len(entries) != 1 {
		t.Errorf("leftovers in the hooks directory: %v", entries)
	}
}

func TestHookBundleRefusesWhatCannotRun(t *testing.T) {
	h := home(t)
	// A program the user did not allow, a script the bundle lacks.
	for what, tc := range map[string]struct {
		command string
		files   map[string]string
		want    string
	}{
		"a program that is not allowed": {"vulnetix agent hook", nil, "hooks.allowed_programs"},
		"a script that is not carried":  {"missing.sh", nil, "missing.sh"},
		"a shell word":                  {"a.sh $HOME", map[string]string{"a.sh": "x\n"}, "plain text"},
		"a path outside":                {"a.sh ../x", map[string]string{"a.sh": "x\n"}, "outside the bundle"},
	} {
		_, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", tc.command)), bundle(tc.files), InstallOptions{})
		if err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", what, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(h, "hooks")); len(entries) != 0 {
		t.Errorf("a refused install left %v", entries)
	}
}

func TestHookBundleAllowedProgramInstallsOnceListed(t *testing.T) {
	home(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "vulnetix"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := config.SaveGlobal(config.Settings{Hooks: &config.HooksSettings{AllowedPrograms: []string{"vulnetix"}}}); err != nil {
		t.Fatal(err)
	}
	res, err := InstallBundle(libitem.Hook, []byte(hookJSON("pix", "vulnetix agent hook")), nil, InstallOptions{Name: "pix"})
	if err != nil {
		t.Fatalf("install: %+v %v", res, err)
	}
	// No file, so the hash is the document's own.
	items, _, _ := List(libitem.Hook)
	if len(items) != 1 || items[0].SHA256 != libitem.Hash(items[0].Doc) || len(items[0].Files) != 0 {
		t.Fatalf("items = %+v", items)
	}
}

func TestHookBundleRefusesBadFilesAndLinks(t *testing.T) {
	h := home(t)
	doc := []byte(hookJSON("guard", "a.sh"))
	good := map[string]string{"a.sh": "x\n"}
	long := map[string]string{"a.sh": "x\n", "b": strings.Repeat("x", MaxBundleFileBytes+1)}
	many := map[string]string{"a.sh": "x\n"}
	for i := 0; i < MaxBundleFiles; i++ {
		many["f"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "x\n"
	}
	for what, files := range map[string]map[string]string{
		"the definition as a file": {"a.sh": "x\n", "hooks.json": "{}"},
		"a parent path":            {"a.sh": "x\n", "../b": "x\n"},
		"an absolute path":         {"a.sh": "x\n", "/etc/x": "x\n"},
		"a dot segment":            {"a.sh": "x\n", ".git/x": "x\n"},
		"a backslash":              {"a.sh": "x\n", `a\b`: "x\n"},
		"an empty file":            {"a.sh": "x\n", "e": ""},
		"a NUL":                    {"a.sh": "x\n", "n": "a\x00b"},
		"a bidi override":          {"a.sh": "x\n", "t": "echo ‮\n"},
		"a file over the limit":    long,
		"too many files":           many,
	} {
		if _, err := InstallBundle(libitem.Hook, doc, bundle(files), InstallOptions{}); err == nil || !IsRefusal(err) {
			t.Errorf("%s: %v", what, err)
		}
	}
	// A hash the library listed must match.
	bad := []BundleFile{{Path: "a.sh", Data: []byte("x\n"), SHA256: strings.Repeat("0", 64)}}
	if _, err := InstallBundle(libitem.Hook, doc, bad, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("a wrong hash: %v", err)
	}
	// Another kind carries no files.
	if _, err := InstallBundle(libitem.Skill, []byte("---\nname: a\ndescription: d\n---\nbody\n"), bundle(good), InstallOptions{}); err == nil || !strings.Contains(err.Error(), "carries no files") {
		t.Errorf("files on a skill: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(h, "hooks")); len(entries) != 0 {
		t.Errorf("refused installs left %v", entries)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// The target and the root are never written through a link.
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h, "hooks", "guard")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := InstallBundle(libitem.Hook, doc, bundle(good), InstallOptions{Overwrite: true}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked target: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was written through the link: %v", entries)
	}
}

func TestHookListSkipsWhatItCannotHold(t *testing.T) {
	h := home(t)
	root := filepath.Join(h, "hooks")
	os.MkdirAll(filepath.Join(root, "nodef"), 0o700)
	os.MkdirAll(filepath.Join(root, "renamed"), 0o700)
	os.WriteFile(filepath.Join(root, "renamed", "hooks.json"), []byte(hookJSON("other", "a.sh")), 0o600)
	os.MkdirAll(filepath.Join(root, "handwritten"), 0o700)
	os.WriteFile(filepath.Join(root, "handwritten", "hooks.json"), []byte(`{"_comment":"x","hooks":{}}`), 0o600)
	os.MkdirAll(filepath.Join(root, "linky"), 0o700)
	os.WriteFile(filepath.Join(root, "linky", "hooks.json"), []byte(hookJSON("linky", "a.sh")), 0o600)
	os.WriteFile(filepath.Join(root, "linky", "a.sh"), []byte("x\n"), 0o700)
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "linky", "pw")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	os.WriteFile(filepath.Join(root, "flat.json"), []byte(`{"name":"flat","event":"stop","command":"x"}`), 0o600)
	items, skipped, err := List(libitem.Hook)
	if err != nil || len(items) != 0 {
		t.Fatalf("items = %+v %v", items, err)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Name] = s.Reason
	}
	for name, want := range map[string]string{"nodef": "missing", "renamed": "named other", "handwritten": "unknown field", "linky": "symbolic link"} {
		if !strings.Contains(reasons[name], want) {
			t.Errorf("%s reason = %q, want %q", name, reasons[name], want)
		}
	}
	if _, ok := reasons["flat"]; ok {
		t.Error("a flat hook file was listed as a bundle")
	}
}

func TestRemoveBundle(t *testing.T) {
	h := home(t)
	if _, err := InstallBundle(libitem.Hook, []byte(hookJSON("guard", "a.sh")), bundle(map[string]string{"a.sh": "x\n"}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveBundle("guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h, "hooks", "guard")); err == nil {
		t.Error("the bundle is still there")
	}
	if err := RemoveBundle("guard"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
	if err := RemoveBundle("../x"); err == nil || !IsRefusal(err) {
		t.Errorf("a bad name: %v", err)
	}
}

func nilPolicy() posture.Policy { return posture.Defaults() }
