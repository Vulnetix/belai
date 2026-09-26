package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/tools"
)

const draftArgs = `{"name":"fixtures","description":"Regenerate fixtures","body":"1. run it"}`

func newDraftSession(t *testing.T, dir string, allowAsk, askDisabled bool) (*Session, func()) {
	t.Helper()
	srv := mockSecurityServer("SkillDraft", draftArgs, "done")
	sess, err := NewSession(Options{
		Cfg:         run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:      srv.Client(),
		Registry:    tools.NewRegistry(tools.SkillDraft{Dir: func() (string, error) { return dir, nil }}),
		Posture:     posture.Defaults(),
		Workdir:     t.TempDir(),
		AllowAsk:    allowAsk,
		AskDisabled: askDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess, srv.Close
}

// SkillDraft asks even with the ask gate off, and writes only on approval.
func TestSkillDraftAsksWithAskGateOff(t *testing.T) {
	dir := t.TempDir()
	sess, done := newDraftSession(t, dir, true, true)
	defer done()
	asked := false
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "save a skill"}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			asked = true
			if e.Ask.Preview == nil {
				t.Error("SkillDraft ask carried no file preview")
			}
			e.AskReply <- PermissionAskReply{Allow: false}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("SkillDraft did not ask with the ask gate off")
	}
	if _, err := os.Stat(filepath.Join(dir, "fixtures", "SKILL.md")); err == nil {
		t.Fatal("a denied SkillDraft wrote the skill")
	}
}

// Without a TTY nobody can approve, so the call is withheld.
func TestSkillDraftWithheldWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	sess, done := newDraftSession(t, dir, false, true)
	defer done()
	var result string
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "save a skill"}, false, func(e Event) {
		if e.Kind == EventToolResultKind {
			result = e.ToolResult
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "needs the user's approval") {
		t.Fatalf("result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "fixtures", "SKILL.md")); err == nil {
		t.Fatal("SkillDraft wrote without an approval")
	}
}
