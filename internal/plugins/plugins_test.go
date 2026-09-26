package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func samplePlugin(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	write(t, src, ManifestFile, `{"name":"go-team","version":"1.0.0","skills":["skills"],"hooks":["hooks"],"prompts":["prompts"],"agents":["agents"]}`)
	write(t, src, "skills/release/SKILL.md", "---\nname: release\ndescription: cut it\n---\nbody\n")
	write(t, src, "hooks/vet.json", `{"name":"vet","event":"post_edit","command":"vet.sh"}`)
	write(t, src, "hooks/vet.sh", "#!/bin/sh\nexit 0\n")
	write(t, src, "prompts/review.md", "review the diff")
	write(t, src, "agents/reviewer.json", `{"name":"reviewer","description":"reviews","system_prompt":"Review the diff.","mode":"single","tools":["Read","Grep"]}`)
	return src
}

func yes(Summary, string, string, *Summary) bool { return true }

func TestInstallLocalAndLoad(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	var shown string
	rec, err := Install(context.Background(), samplePlugin(t), func(s Summary, src, commit string, prev *Summary) bool {
		shown = Describe(s, src, commit, prev)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != "go-team" || rec.Commit != "local" || !rec.Enabled {
		t.Fatalf("record = %+v", rec)
	}
	for _, want := range []string{"release", "vet on post_edit runs vet.sh", "review", "go-team:reviewer"} {
		if !strings.Contains(shown, want) {
			t.Errorf("listing lacks %q:\n%s", want, shown)
		}
	}
	if roots := SkillRoots(); len(roots) != 1 || roots[0].Namespace != "go-team" {
		t.Fatalf("skill roots = %+v", roots)
	}
	if hs := Hooks(posture.Defaults()); len(hs) != 1 || hs[0].Name != "go-team:vet" || !strings.HasSuffix(hs[0].Dir, filepath.Join("go-team", "hooks")) {
		t.Fatalf("hooks = %+v", hs)
	}
	if ps := Prompts(); len(ps) != 1 || ps[0].Name != "go-team:review" {
		t.Fatalf("prompts = %+v", ps)
	}
	if ps := Profiles(); len(ps) != 1 || ps[0].Name != "go-team:reviewer" {
		t.Fatalf("profiles = %+v", ps)
	}
	if err := SetEnabled("go-team", false); err != nil {
		t.Fatal(err)
	}
	if len(SkillRoots())+len(Hooks(posture.Defaults()))+len(Prompts())+len(Profiles()) != 0 {
		t.Fatal("a disabled plugin still loads components")
	}
	if _, err := Install(context.Background(), samplePlugin(t), yes); err == nil {
		t.Fatal("second install of the same name succeeded")
	}
	if err := Remove("go-team"); err != nil {
		t.Fatal(err)
	}
	if recs, _ := List(); len(recs) != 0 {
		t.Fatalf("registry after remove = %+v", recs)
	}
}

func TestDeclineInstallsNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	_, err := Install(context.Background(), samplePlugin(t), func(Summary, string, string, *Summary) bool { return false })
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "plugins"))
	if len(entries) != 0 {
		t.Fatalf("declined install left %v", entries)
	}
}

func TestInvalidComponentRejectsWholePlugin(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	src := samplePlugin(t)
	write(t, src, "hooks/bad.json", `{"name":"bad","event":"pre_tool","command":"/bin/sh"}`)
	if _, err := Install(context.Background(), src, yes); err == nil {
		t.Fatal("plugin with an absolute hook command installed")
	}
}

func TestComponentPathCannotEscape(t *testing.T) {
	src := t.TempDir()
	write(t, src, ManifestFile, `{"name":"x","skills":["../"]}`)
	if _, err := Validate(src); err == nil {
		t.Fatal("escaping component path accepted")
	}
	outside := t.TempDir()
	src2 := t.TempDir()
	write(t, src2, ManifestFile, `{"name":"x","skills":["link"]}`)
	if err := os.Symlink(outside, filepath.Join(src2, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := Validate(src2); err == nil {
		t.Fatal("symlinked component outside the plugin accepted")
	}
}

func TestManifestStrictAndNames(t *testing.T) {
	src := t.TempDir()
	write(t, src, ManifestFile, `{"name":"x","providers":["evil"]}`)
	if _, err := ReadManifest(src); err == nil {
		t.Fatal("unknown manifest key accepted")
	}
	for _, n := range []string{"signet", "user", "Bad", "a/b", ""} {
		if ValidName(n) {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestLocalCopySkipsSymlinks(t *testing.T) {
	src := samplePlugin(t)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("s"), 0o600)
	if err := os.Symlink(secret, filepath.Join(src, "prompts", "leak.md")); err != nil {
		t.Skip(err)
	}
	dest := filepath.Join(t.TempDir(), "d")
	if err := copyTree(src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "prompts", "leak.md")); err == nil {
		t.Fatal("symlink copied into the plugin")
	}
}

func TestUpdateReconfirms(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	src := samplePlugin(t)
	if _, err := Install(context.Background(), src, yes); err != nil {
		t.Fatal(err)
	}
	write(t, src, "prompts/second.md", "two")
	var sawPrev bool
	if _, err := Update(context.Background(), "go-team", "", func(s Summary, _, _ string, prev *Summary) bool {
		sawPrev = prev != nil && len(prev.Prompts) == 1 && len(s.Prompts) == 2
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !sawPrev || len(Prompts()) != 2 {
		t.Fatalf("update did not show the old listing or apply: %v %d", sawPrev, len(Prompts()))
	}
}

func TestDescribeStripsControls(t *testing.T) {
	s := Summary{Manifest: Manifest{Name: "x", Description: "hi\x1b[2Jthere\u202e"}}
	if out := Describe(s, "src", "c", nil); strings.ContainsAny(out, "\x1b\u202e") {
		t.Fatalf("control runes survived: %q", out)
	}
}

func TestGitSourceRejectsOptionInjection(t *testing.T) {
	if _, err := fetch(context.Background(), "https://example.com/x#--upload-pack=evil", filepath.Join(t.TempDir(), "d")); err == nil {
		t.Fatal("option-shaped ref accepted")
	}
}

func TestUpdateRules(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	src := samplePlugin(t)
	if _, err := Install(context.Background(), src, yes); err != nil {
		t.Fatal(err)
	}
	if err := SetEnabled("go-team", false); err != nil {
		t.Fatal(err)
	}
	rec, err := Update(context.Background(), "go-team", "", yes)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Enabled {
		t.Fatal("update re-enabled a disabled plugin")
	}
	other := t.TempDir()
	write(t, other, ManifestFile, `{"name":"someone-else"}`)
	if _, err := Update(context.Background(), "go-team", other, yes); err == nil {
		t.Fatal("update to a source naming another plugin succeeded")
	}
	if _, err := Update(context.Background(), "missing", "", yes); err == nil {
		t.Fatal("update of an uninstalled plugin succeeded")
	}
}

func TestComponentMustBeADirectory(t *testing.T) {
	src := t.TempDir()
	write(t, src, ManifestFile, `{"name":"x","prompts":["file.md"]}`)
	write(t, src, "file.md", "hi")
	if _, err := Validate(src); err == nil {
		t.Fatal("file component accepted")
	}
	src2 := t.TempDir()
	write(t, src2, ManifestFile, `{"name":"x","prompts":["missing"]}`)
	if _, err := Validate(src2); err == nil {
		t.Fatal("missing component accepted")
	}
}

func TestPromptFilesSkippedWhenBadNameOrTooLarge(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "ok.md", "fine")
	write(t, dir, "Bad Name.md", "x")
	write(t, dir, "huge.md", strings.Repeat("a", 64*1024+1))
	ps := readPrompts(dir)
	if len(ps) != 1 || ps[0].Name != "ok" {
		t.Fatalf("prompts = %+v", ps)
	}
}
