package trustgate

import (
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestCheckUnknownDirNeedsPrompt(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	st, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if st.Trusted {
		t.Fatal("fresh dir must not be trusted")
	}
	if !st.NeedsPrompt() {
		t.Fatal("fresh dir must need a prompt")
	}
}

func TestCheckTrustedDirNoProposalsDoesNotPrompt(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	if err := Grant(dir, nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	st, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Trusted {
		t.Fatal("dir should be trusted")
	}
	if st.NeedsPrompt() {
		t.Fatal("trusted dir with no proposals must not prompt")
	}
}

func TestCheckTrustedDirWithNewProposalPromptsOnlyNew(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	other := t.TempDir()
	if err := Grant(dir, nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := config.SaveProject(dir, config.Settings{WorkspaceDirs: []string{other}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	st, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.NeedsPrompt() {
		t.Fatal("a newly proposed directory must prompt")
	}
	want := mustEvalSymlinks(t, other)
	if len(st.NewDirs) != 1 || st.NewDirs[0] != want {
		t.Fatalf("NewDirs = %v, want [%s]", st.NewDirs, want)
	}
}

func TestCheckDeclinedProposalDoesNotComeBack(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	other := t.TempDir()
	if err := Grant(dir, nil); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := config.SaveProject(dir, config.Settings{WorkspaceDirs: []string{other}}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if err := Refuse(dir, []string{other}); err != nil {
		t.Fatalf("Refuse: %v", err)
	}
	st, err := Check(dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(st.NewDirs) != 0 {
		t.Fatalf("NewDirs = %v, want none (declined)", st.NewDirs)
	}
	if st.NeedsPrompt() {
		t.Fatal("a declined proposal must not re-prompt")
	}
}

func mustEvalSymlinks(t *testing.T, p string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return got
}
