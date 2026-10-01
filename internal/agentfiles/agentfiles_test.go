package agentfiles

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
)

const profileID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

// env gives the test its own home and state directory.
func env(t *testing.T) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BELAI_HOME", filepath.Join(home, ".vulnetix", "belai"))
	repo = filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, repo
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func profile(paths []string, sync ...string) agentprofile.AgentProfile {
	p := agentprofile.AgentProfile{ID: profileID, Name: "builder"}
	if len(paths) > 0 {
		p.Knowledge = &agentprofile.KnowledgeSpec{Paths: paths}
	}
	if len(sync) > 0 {
		ws := agentprofile.WorkspaceSpec{Isolation: "worktree"}
		for _, s := range sync {
			ws.Sync = append(ws.Sync, agentprofile.SyncSpec{Path: s, Access: "write"})
		}
		p.Workspace = &ws
	}
	return p
}

func byPath(files []File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Content)
	}
	return out
}

func TestCaptureReadsRelativeHomeAndAbsolutePathsAndDirectories(t *testing.T) {
	home, repo := env(t)
	write(t, filepath.Join(repo, ".vulnetix", "crews", "aws-infra.md"), "crew notes")
	write(t, filepath.Join(repo, "docs", "a.md"), "doc a")
	write(t, filepath.Join(repo, "docs", "sub", "b.md"), "doc b")
	write(t, filepath.Join(home, "handbook", "h.md"), "handbook")
	abs := filepath.Join(t.TempDir(), "std.md")
	write(t, abs, "standards")

	p := profile([]string{".vulnetix/crews/aws-infra.md", "docs", "~/handbook", abs}, ".vulnetix/crews/aws-infra.md")
	files, rep, err := Capture(context.Background(), p, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(files)
	want := map[string]string{
		".vulnetix/crews/aws-infra.md": "crew notes",
		"docs/a.md":                    "doc a",
		"docs/sub/b.md":                "doc b",
		"~/handbook/h.md":              "handbook",
		abs:                            "standards",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if rep.Total() != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, f := range files {
		if f.SHA256 != Sum(f.Content) || len(f.SHA256) != 64 {
			t.Fatalf("%s hash %q", f.Path, f.SHA256)
		}
	}
}

func TestCaptureLeavesOutWhatTheRulesNeverRead(t *testing.T) {
	home, repo := env(t)
	write(t, filepath.Join(repo, "ok.md"), "fine")
	write(t, filepath.Join(repo, "docs", "key.pem"), "not a doc")
	write(t, filepath.Join(repo, "docs", "keep.md"), "kept")
	write(t, filepath.Join(repo, "docs", "leak.md"), "x\n-----BEGIN RSA PRIVATE KEY-----\nabc\n")
	write(t, filepath.Join(repo, "docs", "token.md"), "token AKIAABCDEFGHIJKLMNOP here")
	write(t, filepath.Join(repo, "docs", "bin.md"), "a\x00b")
	write(t, filepath.Join(repo, "docs", "empty.md"), "")
	write(t, filepath.Join(repo, "docs", "big.md"), strings.Repeat("x", MaxFileBytes+1))
	write(t, filepath.Join(home, ".ssh", "config.md"), "ssh")
	write(t, filepath.Join(repo, ".git", "HEAD.md"), "git")
	outside := filepath.Join(t.TempDir(), "outside.md")
	write(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(repo, "docs", "link.md")); err != nil {
		t.Skip("no symlinks here")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}

	p := profile([]string{"ok.md", "docs", "~/.ssh/config.md", ".git/HEAD.md", "linked", "missing.md", "../escape.md"})
	files, rep, err := Capture(context.Background(), p, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(files)
	if len(got) != 2 || got["ok.md"] != "fine" || got["docs/keep.md"] != "kept" {
		t.Fatalf("captured %v", got)
	}
	if rep.Secrets != 2 {
		t.Fatalf("secrets = %d, want 2 (%+v)", rep.Secrets, rep)
	}
	if rep.Missing != 1 || rep.Refused < 4 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestCaptureHonoursTheCaps(t *testing.T) {
	_, repo := env(t)
	var paths []string
	for i := 0; i < MaxFiles+4; i++ {
		name := "d/f" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".md"
		write(t, filepath.Join(repo, name), "x")
		paths = append(paths, name)
	}
	files, rep, _ := Capture(context.Background(), profile(paths), []string{repo})
	if len(files) != MaxFiles || rep.OverLimit != 4 {
		t.Fatalf("%d files, report %+v", len(files), rep)
	}
	// Total bytes: 9 files of 240 KiB would pass the count and break the total.
	var big []string
	for i := 0; i < 9; i++ {
		name := "big" + string(rune('a'+i)) + ".md"
		write(t, filepath.Join(repo, name), strings.Repeat("y", 240<<10))
		big = append(big, name)
	}
	files, rep, _ = Capture(context.Background(), profile(big), []string{repo})
	total := 0
	for _, f := range files {
		total += len(f.Content)
	}
	if total > MaxTotalBytes || rep.OverLimit == 0 {
		t.Fatalf("total %d, report %+v", total, rep)
	}
}

func TestInstallWritesOnlyInsideTheOwnedDirAndEnumerationFindsIt(t *testing.T) {
	home, _ := env(t)
	files := []File{
		{Path: ".vulnetix/crews/aws-infra.md", Content: []byte("crew notes")},
		{Path: "~/handbook/h.md", Content: []byte("handbook")},
		{Path: "/srv/std.md", Content: []byte("standards")},
		{Path: "docs/a.md", Content: []byte("doc a")},
	}
	for i := range files {
		files[i].SHA256 = Sum(files[i].Content)
	}
	if err := Install(profileID, files); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.ProfileFilesDir(profileID)
	for rel, want := range map[string]string{
		"rel/.vulnetix/crews/aws-infra.md": "crew notes", "home/handbook/h.md": "handbook", "abs/srv/std.md": "standards", "rel/docs/a.md": "doc a",
	} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q %v", rel, b, err)
		}
	}
	// The profile's own paths resolve to the owned copy where this host has none.
	got, skipped, err := knowledge.EnumerateProfileOwned(context.Background(), "", dir, []string{".vulnetix/crews/aws-infra.md", "~/handbook", "/srv/std.md", "docs"})
	if err != nil || skipped != 0 || len(got) != 4 {
		t.Fatalf("enumerate: %d files, %d skipped, %v", len(got), skipped, err)
	}
	// A copy of the file in the project wins over the owned one.
	repo := filepath.Join(home, "proj")
	write(t, filepath.Join(repo, "docs", "a.md"), "project copy")
	got, _, _ = knowledge.EnumerateProfileOwned(context.Background(), repo, dir, []string{"docs/a.md"})
	if len(got) != 1 || !strings.HasPrefix(got[0].Abs, repo) {
		t.Fatalf("the project's own file should win: %+v", got)
	}
	// Without the owned directory the same paths name nothing.
	if got, skipped, _ = knowledge.EnumerateProfile(context.Background(), "", []string{"docs/a.md"}); len(got) != 0 || skipped != 1 {
		t.Fatalf("no owned dir: %d files, %d skipped", len(got), skipped)
	}
}

func TestInstallRefusesBeforeWritingAnything(t *testing.T) {
	env(t)
	ok := File{Path: "keep.md", Content: []byte("keep")}
	if err := Install(profileID, []File{ok}); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.ProfileFilesDir(profileID)
	bad := map[string][]File{
		"climbs out":  {ok, {Path: "../x.md", Content: []byte("x")}},
		"climbs in":   {ok, {Path: "a/../../x.md", Content: []byte("x")}},
		"empty path":  {ok, {Path: "", Content: []byte("x")}},
		"root":        {ok, {Path: "/", Content: []byte("x")}},
		"dot":         {ok, {Path: ".", Content: []byte("x")}},
		"home itself": {ok, {Path: "~/", Content: []byte("x")}},
		"duplicate":   {{Path: "a.md", Content: []byte("1")}, {Path: "a.md", Content: []byte("2")}},
		"binary":      {ok, {Path: "b.md", Content: []byte("a\x00b")}},
		"not utf-8":   {ok, {Path: "b.md", Content: []byte{0xff, 0xfe}}},
		"empty file":  {ok, {Path: "b.md", Content: nil}},
		"too big":     {ok, {Path: "b.md", Content: bytes.Repeat([]byte("x"), MaxFileBytes+1)}},
		"wrong hash":  {ok, {Path: "b.md", Content: []byte("b"), SHA256: Sum([]byte("other"))}},
		"too many":    manyFiles(MaxFiles + 1),
	}
	for name, files := range bad {
		err := Install(profileID, files)
		if err == nil || !errors.Is(err, ErrBadFile) {
			t.Errorf("%s: %v", name, err)
		}
		if b, rerr := os.ReadFile(filepath.Join(dir, "rel", "keep.md")); rerr != nil || string(b) != "keep" {
			t.Fatalf("%s: a refused install changed the old files (%q, %v)", name, b, rerr)
		}
	}
	// Nothing was written outside the directory.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "x.md")); err == nil {
		t.Fatal("a file escaped the owned directory")
	}
	// A profile id that is not an id has no directory.
	if err := Install("../etc", []File{ok}); err == nil {
		t.Fatal("a bad profile id was accepted")
	}
}

func manyFiles(n int) []File {
	out := make([]File, n)
	for i := range out {
		out[i] = File{Path: "f" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".md", Content: []byte("x")}
	}
	return out
}

func TestInstallReplacesTheWholeDirectoryAndEmptyRemovesIt(t *testing.T) {
	env(t)
	if err := Install(profileID, []File{{Path: "old.md", Content: []byte("old")}, {Path: "both.md", Content: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	if err := Install(profileID, []File{{Path: "both.md", Content: []byte("2")}, {Path: "new.md", Content: []byte("new")}}); err != nil {
		t.Fatal(err)
	}
	dir, _ := config.ProfileFilesDir(profileID)
	if _, err := os.Stat(filepath.Join(dir, "rel", "old.md")); err == nil {
		t.Fatal("a file the new version dropped is still there")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "rel", "both.md")); string(b) != "2" {
		t.Fatalf("both.md = %q", b)
	}
	if _, err := os.Stat(dir + ".old"); err == nil {
		t.Fatal("the swap left its backup behind")
	}
	if err := Install(profileID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("an empty install should remove the directory")
	}
}

func TestCaptureReadsBackWhatAnInstallWrote(t *testing.T) {
	env(t)
	in := []File{{Path: "~/handbook/h.md", Content: []byte("handbook")}, {Path: "notes.md", Content: []byte("notes")}}
	if err := Install(profileID, in); err != nil {
		t.Fatal(err)
	}
	// This host has neither path itself, so the owned copy is what a second
	// backup captures.
	files, rep, err := Capture(context.Background(), profile([]string{"~/handbook", "notes.md"}), nil)
	got := byPath(files)
	if err != nil || rep.Total() != 0 || got["~/handbook/h.md"] != "handbook" || got["notes.md"] != "notes" {
		t.Fatalf("captured %v (%+v, %v)", got, rep, err)
	}
}

func TestListedPathsAreKnowledgeThenSyncOnce(t *testing.T) {
	p := profile([]string{"a.md", "b/"}, "b/", "c.md")
	got := ListedPaths(p)
	if strings.Join(got, ",") != "a.md,b/,c.md" {
		t.Fatalf("paths = %v", got)
	}
}

func TestHoldsSecret(t *testing.T) {
	for _, s := range []string{
		"-----BEGIN PRIVATE KEY-----", "-----BEGIN OPENSSH PRIVATE KEY-----", "AKIAABCDEFGHIJKLMNOP",
		"ghp_" + strings.Repeat("a", 36), "xoxb-1234567890-abcdef", "glpat-" + strings.Repeat("a", 20),
	} {
		if !HoldsSecret([]byte("x " + s + " y")) {
			t.Errorf("%q was not seen as a secret", s)
		}
	}
	for _, s := range []string{"a public key -----BEGIN PUBLIC KEY-----", "AKIA is a prefix", "plain prose about tokens", "arn:aws:iam::061882422009:role/Auditor"} {
		if HoldsSecret([]byte(s)) {
			t.Errorf("%q was taken for a secret", s)
		}
	}
}
